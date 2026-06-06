// Package build implements `pg_sandbox build <version>` — compiling a
// PostgreSQL major.minor release from the official source tarball into
// a versioned install dir. SPEC §7.1.
//
// Pipeline (all stages stream their stdout+stderr into per-step log
// files under the build dir so a post-mortem doesn't require re-running
// anything):
//
//  1. Validate the requested version against ^[0-9]+\.[0-9]+$ — anything
//     else is a typo and we refuse with ExitUsage. We never touch the
//     network on a malformed version.
//  2. Resolve paths: install prefix (PGS_BIN_DIR/<ver>/, OR PGS_BIN_DIR
//     itself when its basename already matches major.minor — to avoid
//     surprising `/opt/pg/18.4/18.4`-style double-nesting; a mismatch
//     between that basename and the build version emits a warning),
//     build scratch (PGS_BUILD_DIR or $TMPDIR/pg_sandbox-build/),
//     per-step log dir.
//  3. Handle --force: if the install prefix already exists, abort
//     (ExitBuildFailed) unless --force was passed; with --force we
//     RemoveAll the existing prefix so make install has a clean target.
//  4. Download the tarball over HTTPS from ftp.postgresql.org. A
//     non-200 response is treated as "wrong version" (the most common
//     user error) and reported as such. A non-zero cached tarball is
//     reused to avoid re-downloading on repeated builds.
//  5. Extract via tar -xzf. We shell out rather than using
//     archive/tar+compress/gzip because tar handles symlinks,
//     permissions, and large archives more robustly than a hand-
//     rolled extractor — and SPEC §4.8 explicitly lists tar as a
//     dependency.
//  6. ./configure with --prefix=<installPrefix>. ICU is OFF by default
//     to match the Python tool; --with-icu/--with-openssl/--configure-opts
//     append flags. PGS_BUILD_DEBUG=1 adds --enable-cassert
//     --enable-debug and CFLAGS=-O0 -g3 so users can build a debuggable
//     server.
//  7. make -j<N>, default runtime.NumCPU().
//  8. make install.
//  9. cd contrib && make && make install. Contrib failure is treated
//     the same as core failure — partial installs are confusing.
//  10. Delete the extracted source dir on success (leave the cached
//     tarball so a rebuild is fast).
//
// On success we print the install prefix to STDOUT (so users can do
// `pg_sandbox deploy -b "$(pg_sandbox build 18.4)/bin"`) and a
// structured "built" line to STDERR.
//
// Cancellation: the caller's ctx is plumbed through every child
// process. A SIGINT at the CLI layer cancels the context, which
// kills the running compiler. We do not impose internal timeouts —
// compilation routinely takes minutes and the only sensible budget
// is what the user signals.
package build
