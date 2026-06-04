// argv pre-processing helpers for the subcommand layer.
//
// Go's standard library `flag` package stops parsing at the first
// non-flag argument. That makes the very common invocation
//
//	pg_sandbox cleanup-install-versions 18.3 --force
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
// The `--` separator is honored as a verbatim-end marker: every
// token from `--` onward is appended to the tail of the returned
// slice unchanged, even if it would otherwise be a known bool flag.
// This mirrors the GNU getopt convention that callers use to force
// later tokens to be treated as positionals.
//
// LIMITATIONS (intentional — caller is responsible):
//   - Only BOOLEAN flags. A flag that takes a value (e.g. `--root
//     /path`) requires its value to appear immediately after it, so
//     reordering would break the pair. Don't put value-taking flag
//     names in `knownBoolFlags`. Production callers should pass
//     `boolFlagNames(fs)` to derive the list from a parsed FlagSet,
//     which guarantees this caveat automatically (BoolVar flags are
//     bool; StringVar/IntVar/etc. are not). Tests may still pass a
//     hand-written bare-name list directly.
//
// The helper is intentionally narrow: it fixes the positional-before-
// bool-flag UX on a per-subcommand basis without changing the global
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
	// Must start with a dash to be a flag at all.
	if !strings.HasPrefix(tok, "-") {
		return false
	}
	// Strip the leading `--` or `-`. Go's `flag` package treats both
	// the same, so we do too.
	name := strings.TrimPrefix(tok, "-")
	name = strings.TrimPrefix(name, "-")
	// Drop any `=value` suffix; Go's flag accepts `--force=true`.
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
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
