// Package cleanup implements `pg_sandbox cleanup-install-versions` —
// pruning PostgreSQL install directories under PGS_BIN_DIR that are
// not referenced by any sandbox on the host. SPEC §7.2.
//
// The algorithm is two pass:
//
//  1. Inventory the candidate versions by listing PGS_BIN_DIR's
//     version-shaped subdirectories (major.minor, e.g. "16.4" —
//     matching what `build` installs). Non-version dirs are never
//     candidates, and a PGS_BIN_DIR that itself looks like a single
//     install prefix (contains bin/ and lib/) is refused outright.
//  2. Walk the sandbox root and load every sandbox config file. A
//     candidate version is "in use" iff some sandbox's binDir is the
//     version dir or lives under it. Both sides are compared cleaned
//     AND symlink-resolved (filepath.EvalSymlinks, best-effort) so a
//     sandbox reaching the install through a symlink — `latest ->
//     16.4`, or macOS's /tmp -> /private/tmp — still counts.
//
// Then we either prompt the user with a y/N (unless --force) and rm
// -rf the unused versions, or refuse with ExitNotATTY when stdin
// isn't a TTY and --force wasn't set (SPEC §4.7). Between the
// confirmation and each removal, Apply re-walks the sandbox root and
// skips any candidate that became in-use while the prompt sat open
// (TOCTOU guard).
package cleanup
