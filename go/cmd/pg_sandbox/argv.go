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
// version they wanted). On 2026-06-04 a Phase-2 smoke test was
// bitten by exactly this — see the project memory
// `cleanup-install-versions-pitfall.md`.
//
// The helper below is a tiny, opt-in workaround: a per-command list
// of known boolean flag names, moved to the front of argv before
// `flag.FlagSet.Parse` sees it. We do NOT touch other commands here
// because each command has its own mix of bool / value-taking flags
// and would need per-command testing.

package main

// reorderBoolFlags moves any token in `argv` that exactly matches an
// entry in `knownBoolFlags` to the front of the returned slice, in
// the order encountered. Positionals (anything else) keep their
// original relative order and follow the flags.
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
//     names in `knownBoolFlags`.
//   - The caller must enumerate the known bool flag names (both the
//     long and short forms, e.g. `--force` AND `-f`). We do not
//     reflect over a *flag.FlagSet because that couples the helper
//     to the package's parsing choices and complicates testing.
//   - Tokens are compared by exact string match. `--force=true` is
//     not recognized; cleanup-install-versions doesn't currently
//     accept that form anyway.
//
// The helper is intentionally narrow: it fixes the cleanup-install-
// versions UX without changing the global CLI surface.
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
		if known[tok] {
			flags = append(flags, tok)
			continue
		}
		positionals = append(positionals, tok)
	}
	return append(flags, positionals...)
}
