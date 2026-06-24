// argv pre-processing helpers for the subcommand layer.
//
// Go's standard library `flag` package stops parsing at the first
// non-flag argument. That makes the very common invocation
//
//	pg_sandbox cleanup-install-versions 18.4 --force
//
// surprising: `--force` is treated as a positional, not a flag, and
// the destructive prompt that --force was meant to suppress fires
// (or worse, the user types "y" thinking they're confirming the
// version they wanted).
//
// The helper below is a tiny, opt-in workaround: known boolean flag
// names are moved to the front of argv before `flag.FlagSet.Parse`
// sees it. The typical caller derives the list of bool flag names
// automatically from its FlagSet via boolFlagNames, which uses the
// stdlib `IsBoolFlag() bool` interface to separate bool from value-
// taking flags — so the helper is general-purpose and type-safe.
//
// Any subcommand that mixes a positional with one or more BoolVar
// flags SHOULD call `reorderBoolFlags(args, boolFlagNames(fs))`
// immediately before `fs.Parse(args)`; without it the natural
// invocation `pg_sandbox <cmd> <positional> --bool-flag` silently
// misbehaves. The only requirement on the caller is that every
// BoolVar this command cares about is registered on the FlagSet
// BEFORE the helper is called (boolFlagNames reads the FlagSet's
// current registrations).

package main

import (
	"flag"
	"strings"
)

// reorderBoolFlags moves any token in `argv` that names a known
// boolean flag to the front of the returned slice, in the order
// encountered. Positionals (anything else) keep their original
// relative order and follow the flags.
//
// `knownBoolFlags` is a list of BARE flag names (no leading dashes),
// e.g. `[]string{"force", "f"}`. A token matches if, after stripping
// a single leading `-` or `--` and any trailing `=…` suffix, the
// remaining bare name is in the list. So with `["force", "f"]` the
// tokens `--force`, `-force`, `--force=true`, `--force=false`, `-f`,
// `-f=true`, and `--f=false` all match; `--forces`, `--root`, and a
// bare `force` do not. (`--force=` with an empty value still matches;
// Go's flag parser will then error on the empty value, which is its
// job, not ours.) The ORIGINAL token (with dashes and any `=value`
// suffix) is what gets promoted — Parse needs to see what the user
// typed.
//
// The `--` separator is preserved verbatim: every token from `--`
// onward is appended to the tail of the returned slice unchanged,
// in input order, even if it would otherwise be a known bool flag.
// This helper does NOT, on its own, deliver GNU-getopt-style end-
// of-options semantics — `flag.Parse` stops at the first non-flag,
// so `--` only acts as an end-of-options marker downstream if no
// positional precedes it after the reorder. In practice that means
// `cmd --flag -- positional` works as expected (reorder is a no-op,
// Parse sees `--flag` then `--` and stops), but `cmd positional1 --
// positional2` will still have Parse stop at `positional1` and the
// `--` itself will be left as a positional for the subcommand to
// reject or consume. Subcommands that accept arbitrary positionals
// before `--` need their own end-of-options handling on top of this
// helper.
//
// Value-taking flags are handled separately by reorderValueFlags and
// parseSubcommandArgs runs value reorder before bool reorder so
// invocations like `build 17.9 -b /opt/postgresql` work.
//
// LIMITATIONS (intentional — caller is responsible):
//   - reorderBoolFlags only promotes BOOLEAN flags. Value-taking
//     flags belong in reorderValueFlags via valueFlagNames(fs).
//   - Only subcommands that call parseSubcommandArgs get both
//     reorders; others still require flags before positionals.
//
// The helpers are intentionally narrow: they fix the positional-before-
// flag UX on a per-subcommand basis without changing the global
// CLI surface (no rewriting of os.Args, no global pre-parse pass).
func reorderBoolFlags(argv []string, knownBoolFlags []string) []string {
	known := make(map[string]bool, len(knownBoolFlags))
	for _, f := range knownBoolFlags {
		known[f] = true
	}

	flags := make([]string, 0, len(argv))
	positionals := make([]string, 0, len(argv))
	for i, tok := range argv {
		if tok == "--" {
			// Preserve `--` and everything after it verbatim at the
			// end. Positionals collected so far stay between the
			// flags and the verbatim tail.
			out := append(flags, positionals...)
			out = append(out, argv[i:]...)
			return out
		}
		if isKnownBoolFlagToken(tok, known) {
			flags = append(flags, tok)
			continue
		}
		positionals = append(positionals, tok)
	}
	return append(flags, positionals...)
}

// isKnownBoolFlagToken reports whether `tok` is a flag-shaped token
// whose bare name (after stripping a single leading `-` or `--` and
// any trailing `=…` suffix) is in `known`. A bare word with no
// leading dash (e.g. `force`) is not a flag and never matches.
func isKnownBoolFlagToken(tok string, known map[string]bool) bool {
	name := bareFlagName(tok)
	if name == "" {
		return false
	}
	return known[name]
}

// boolFlagNames returns the bare names of every flag registered on
// `fs` whose Value is a boolean flag, as reported by the canonical
// stdlib idiom `interface{ IsBoolFlag() bool }`. The result is
// suitable for passing to reorderBoolFlags, so callers don't need to
// hand-maintain a parallel list when adding a new BoolVar.
//
// Iteration order: fs.VisitAll walks flags in lexicographic name
// order. reorderBoolFlags only cares about set membership, so the
// order is irrelevant for correctness.
func boolFlagNames(fs *flag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			names = append(names, f.Name)
		}
	})
	return names
}

// reorderValueFlags moves any token in `argv` that names a known
// value-taking flag to the front of the returned slice, together with
// its value when the value is a separate token. Positionals keep
// their relative order and follow the promoted flag groups.
//
// Inline `--flag=value` forms are promoted as a single token. For
// bare `--flag` / `-f` forms the immediately following token is
// consumed as the value unless it is `--`.
func reorderValueFlags(argv []string, knownValueFlags []string) []string {
	known := make(map[string]bool, len(knownValueFlags))
	for _, f := range knownValueFlags {
		known[f] = true
	}

	flags := make([]string, 0, len(argv))
	positionals := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if tok == "--" {
			out := append(flags, positionals...)
			out = append(out, argv[i:]...)
			return out
		}
		if isKnownValueFlagToken(tok, known) {
			flags = append(flags, tok)
			if !valueFlagHasInlineValue(tok) && i+1 < len(argv) && argv[i+1] != "--" {
				i++
				flags = append(flags, argv[i])
			}
			continue
		}
		positionals = append(positionals, tok)
	}
	return append(flags, positionals...)
}

// isKnownValueFlagToken reports whether tok names a known value flag.
func isKnownValueFlagToken(tok string, known map[string]bool) bool {
	name := bareFlagName(tok)
	if name == "" {
		return false
	}
	return known[name]
}

// valueFlagHasInlineValue reports whether tok uses `--name=value` form.
func valueFlagHasInlineValue(tok string) bool {
	name := bareFlagName(tok)
	if name == "" {
		return false
	}
	// bareFlagName strips =value; compare original stripped prefix.
	if !strings.HasPrefix(tok, "-") {
		return false
	}
	rest := strings.TrimPrefix(tok, "-")
	rest = strings.TrimPrefix(rest, "-")
	return strings.Contains(rest, "=")
}

// bareFlagName returns the flag name from tok, or "" if tok is not
// flag-shaped. Strips one or two leading dashes and any =value suffix.
func bareFlagName(tok string) string {
	if !strings.HasPrefix(tok, "-") {
		return ""
	}
	name := strings.TrimPrefix(tok, "-")
	name = strings.TrimPrefix(name, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	return name
}

// valueFlagNames returns bare names of every non-boolean flag on fs.
func valueFlagNames(fs *flag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			return
		}
		names = append(names, f.Name)
	})
	return names
}

// parseSubcommandArgs is the canonical wrapper that reorders value-
// and bool-flags in `args` to the front of the slice (so positionals
// before flags don't make `flag.Parse` stop early) and then calls
// fs.Parse. Subcommands that mix positional args with flags should
// call this instead of fs.Parse directly.
//
// Why a wrapper instead of "remember to call reorderBoolFlags then
// fs.Parse"? The ordering is load-bearing: boolFlagNames must run
// AFTER every BoolVar registration but BEFORE Parse. A future
// contributor who inserts a new `fs.BoolVar(&x, "dry-run", …)`
// between a manual reorder call and a manual Parse call would
// silently regress the positional-before-bool UX for that flag —
// boolFlagNames already ran, the frozen result misses "dry-run", and
// `<cmd> 18.4 --dry-run` treats --dry-run as a positional again. By
// folding the two steps into one call we make the mis-sequencing
// structurally impossible: every BoolVar registered on `fs` before
// this call is picked up automatically, there is no parallel
// enumeration to keep in sync, and inserting a new BoolVar between
// `fs.BoolVar(...)` lines and this call is the only supported shape.
func parseSubcommandArgs(fs *flag.FlagSet, args []string) error {
	args = reorderValueFlags(args, valueFlagNames(fs))
	args = reorderBoolFlags(args, boolFlagNames(fs))
	return fs.Parse(args)
}
