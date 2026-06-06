// Module declaration for postgresql_sandbox (Go).
//
// Go version: 1.22 — minimum required for `log/slog` (1.21+), the
// improved loop variable semantics (1.22), and the new math/rand/v2
// (1.22). Stable, widely deployed baseline as of writing.
//
// Dependencies policy: the standard library only. No external Go
// modules. If a future requirement makes an external module
// unavoidable, the decision MUST be recorded in SPEC.md and the
// module added here explicitly — not pulled in transitively.

module github.com/guriandoro/postgresql_sandbox

go 1.22
