// Package pgexec wraps the external PostgreSQL binaries the tool
// shells out to: initdb, pg_ctl, psql, pg_basebackup, pg_dump,
// pg_config. The package exposes a small, testable interface so the
// rest of the tool can construct calls without touching os/exec
// directly, and so component tests can substitute fakes.
//
// See SPEC.md §4.8 for the binaries this package wraps.
package pgexec
