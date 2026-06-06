# pg_sandbox (Go port)

> **Status:** Broadly functional. Every documented subcommand has a real handler — single-sandbox lifecycle, replication (physical + logical), cluster orchestration, config, reporting, source build, and install pruning are all wired in. The full functional specification is in [`SPEC.md`](./SPEC.md) — that document is still the contract for what this port must do.

`pg_sandbox` is a command-line tool that provisions, manages, and tears down local PostgreSQL sandbox instances for development, testing, and bug reproduction. This directory hosts a Go re-implementation of the original Python tool (which lives at the repository root and continues to be the working version until this port reaches parity).

Design goals for the port:

- **Zero external Go module dependencies.** Standard library only.
- **Cross-platform.** macOS and Linux, both `amd64` and `arm64`, statically cross-compiled.
- **Idiomatic Go.** `gofmt`/`go vet` clean, small packages, errors wrapped with `%w`, structured logging via `log/slog`, `context.Context` plumbed through long operations.
- **Fully documented.** SPEC drives behavior; every package has a doc comment; every exported identifier has a doc comment.

The Python sources at the repository root are intentionally untouched on this branch. Users can keep using `pg_sandbox` (the Python script) as the working tool while the Go port is built up.

## Project layout

```
go/
├── README.md                  this file
├── SPEC.md                    canonical functional spec — the contract
├── docs/                      user-facing reference (commands, env vars, exit codes, examples)
├── go.mod                     module declaration; no external deps
├── Makefile                   build / test / lint / cross-compile
├── scripts/build.sh           shell mirror of `make build-all`
├── .golangci.yml              optional lint configuration
├── cmd/pg_sandbox/main.go     CLI entry point and subcommand dispatcher
├── internal/                  implementation packages (see below)
└── testdata/                  fixtures for unit + integration tests
```

The `internal/` packages are domain-scoped:

| Package | Responsibility |
|---|---|
| `internal/sandbox` | Single-instance lifecycle (deploy / destroy / start / stop / status) |
| `internal/cluster` | Cluster manifest + multi-member orchestration |
| `internal/replication` | Physical + logical replication primitives |
| `internal/pgexec` | Thin, testable wrappers around `psql` / `pg_ctl` / `initdb` / `pg_basebackup` / `pg_dump` |
| `internal/config` | Per-sandbox and global config: schema, load/save, resolution, migration |
| `internal/portalloc` | Free TCP port detection |
| `internal/report` | `pg_gather` HTML report generation pipeline |
| `internal/ui` | Logging, prompts, exit codes |

The package boundaries are a starting point — implementation may refine them as commands land.

## Requirements

- **Go 1.22 or newer** (for `log/slog` and the new loop variable semantics).
- **PostgreSQL binaries** (`initdb`, `pg_ctl`, `psql`, `pg_basebackup`, `pg_dump`) reachable on disk. The Go port does not bundle PostgreSQL; it points at an existing install via `--bin-dir`.
- **macOS or Linux.** Windows is not supported.

## Build

From this `go/` directory:

```sh
# Local-arch binary into ./bin/pg_sandbox
make build

# All four release binaries into ./bin/
make build-all

# Or, without GNU make:
./scripts/build.sh
```

Each binary is stripped (`-ldflags="-s -w"`) and stamped with the current version + commit (`go/bin/pg_sandbox --version` shows both).

## Test

```sh
make test          # all unit + component tests
make vet           # go vet
make lint          # go vet + golangci-lint (if installed)
make fmt           # gofmt -s -w .
```

Integration smoke tests are opt-in and require a real PostgreSQL install:

```sh
PGS_BIN_DIR=/opt/postgresql/18.4 go test -tags=integration ./...
```

## Documentation

- [`SPEC.md`](./SPEC.md) — **canonical** functional specification. Every implementation PR references a SPEC section.
- [`docs/commands.md`](./docs/commands.md) — per-command reference.
- [`docs/environment.md`](./docs/environment.md) — environment variables.
- [`docs/exit-codes.md`](./docs/exit-codes.md) — exit code reference.
- [`docs/examples.md`](./docs/examples.md) — end-to-end recipes.

## Relationship to the Python tool

Until this Go port is declared GA, both coexist:

- The **Python** tool is the recommended one for real use. It lives at the repository root.
- The **Go** tool is under active development under `go/`. It is broadly functional — most workflows succeed end-to-end — but has not yet been declared the canonical entry point.

Surface-level differences to watch for when switching between the two:

- **Environment variables.** The Python tool reads `PGS_ROOT_DIR`; the Go port reads `PGS_SANDBOX_ROOT` (different name, same role). `PGS_ENV_FILE` does not exist in the Go port — per-sandbox state is JSON (`pg_sandbox.json`), not env-format. `PGS_DEBUG` works in both. `PGS_LOG_LEVEL` / `PGS_CONFIG_FILE` are Python-tool-only — the Go port deliberately collapses log control onto `--debug` / `--quiet` and follows XDG for the global config path. The full Go-port matrix is in [`docs/environment.md`](./docs/environment.md).
- **Per-sandbox state file.** Python writes `pg_sandbox.env` (KEY=VALUE). The Go port writes `pg_sandbox.json` (strict JSON, schema-versioned). `pg_sandbox config migrate` converts a legacy env file into the JSON shape.
- **`cluster deploy --sync-count`.** Accepted in both, but the Go port treats it as async today (synchronous wiring is deferred) and emits a warn-level diagnostic when `k > 0`.

When the Go port is declared GA, the Go binary will take over the canonical `pg_sandbox` name; the Python entry point will be renamed (e.g., `pg_sandbox.py`) and kept around for users mid-migration. That transition is its own change and won't happen as part of this port.
