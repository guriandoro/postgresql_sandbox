// Package cleanup implements `pg_sandbox cleanup-install-versions` —
// pruning PostgreSQL install directories under PGS_BIN_DIR that are
// not referenced by any sandbox on the host. SPEC §7.2.
//
// The algorithm is two pass:
//
//  1. Inventory the candidate versions by listing PGS_BIN_DIR's
//     subdirectories.
//  2. Walk the sandbox root and load every sandbox config file. A
//     candidate version is "in use" iff some sandbox's binDir starts
//     with PGS_BIN_DIR/<version>/. The check is purely string-prefix
//     (after filepath.Clean) — we do not try to follow symlinks or
//     resolve `bin/` vs `bin` distinctions; users who symlink their
//     installs are on their own.
//
// Then we either prompt the user with a y/N (unless --force) and rm
// -rf the unused versions, or refuse with ExitNotATTY when stdin
// isn't a TTY and --force wasn't set (SPEC §4.7).
package cleanup
