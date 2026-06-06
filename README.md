# pg_sandbox

[![CI](https://github.com/guriandoro/postgresql_sandbox/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/guriandoro/postgresql_sandbox/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/guriandoro/postgresql_sandbox?sort=semver)](https://github.com/guriandoro/postgresql_sandbox/releases/latest)

`pg_sandbox` is a command-line tool that provisions, manages, and tears down local PostgreSQL sandbox instances for development, testing, and bug reproduction. It targets macOS and Linux on `amd64` and `arm64`, and ships as a single static binary with no runtime dependencies beyond the PostgreSQL binaries you point it at.

The canonical functional contract lives in [`SPEC.md`](./SPEC.md).

## Install

Pre-built binaries are attached to each [GitHub Release](https://github.com/guriandoro/postgresql_sandbox/releases). Pick the artifact matching your platform:

| Platform | Artifact |
|---|---|
| Linux x86_64 | `pg_sandbox-linux-amd64` |
| Linux ARM64 (Graviton, RPi, ARM cloud VMs) | `pg_sandbox-linux-arm64` |
| macOS Intel | `pg_sandbox-darwin-amd64` |
| macOS Apple Silicon | `pg_sandbox-darwin-arm64` |

```sh
# Example: install the Linux x86_64 binary into /usr/local/bin
curl -L -o /usr/local/bin/pg_sandbox \
  https://github.com/guriandoro/postgresql_sandbox/releases/latest/download/pg_sandbox-linux-amd64
chmod +x /usr/local/bin/pg_sandbox
```

A `SHA256SUMS` file is published alongside each release for checksum verification.

## Build from source

```sh
# Local-arch binary into ./bin/pg_sandbox
make build

# All four release binaries into ./bin/
make build-all

# Or, without GNU make:
./scripts/build.sh
```

Requires **Go 1.22 or newer**. Binaries are stripped (`-ldflags="-s -w"`) and stamped with the current version + commit (`./bin/pg_sandbox --version` shows both). `CGO_ENABLED=0` is set in the cross-compile path, so the Linux artifacts have no glibc dependency and run on any reasonably-recent kernel.

## Requirements

- **Go 1.22+** (only for building from source — pre-built binaries have no Go requirement).
- **PostgreSQL binaries** (`initdb`, `pg_ctl`, `psql`, `pg_basebackup`, `pg_dump`) reachable on disk. `pg_sandbox` does not bundle PostgreSQL; it points at an existing install via `--bin-dir` or `PGS_BIN_DIR`.
- **macOS or Linux.** Windows is not supported.

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

## Project layout

```
.
├── README.md                  this file
├── SPEC.md                    canonical functional spec — the contract
├── docs/                      user-facing reference (commands, env vars, exit codes, examples)
├── go.mod                     module declaration; no external deps
├── Makefile                   build / test / lint / cross-compile
├── scripts/build.sh           shell mirror of `make build-all`
├── .golangci.yml              optional lint configuration
├── cmd/pg_sandbox/main.go     CLI entry point and subcommand dispatcher
├── internal/                  implementation packages (see below)
├── testdata/                  fixtures for unit + integration tests
└── deprecated/                archival snapshot of the Python tool — see below
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

## Documentation

- [`SPEC.md`](./SPEC.md) — **canonical** functional specification.
- [`docs/commands.md`](./docs/commands.md) — per-command reference.
- [`docs/environment.md`](./docs/environment.md) — environment variables.
- [`docs/exit-codes.md`](./docs/exit-codes.md) — exit code reference.
- [`docs/examples.md`](./docs/examples.md) — end-to-end recipes.

## Deprecated Python tool

This project previously shipped as a Python script (`pg_sandbox`, `pg_sandbox_help.py`, `pg_sandbox_errors.py`) packaged via PyInstaller. That implementation has been superseded by the Go port and is no longer maintained.

An archival snapshot of the Python sources and their docs is kept under [`deprecated/`](./deprecated/) for users mid-migration. The full Python commit history is reachable via the `python-final` git tag.

The two implementations are **not drop-in compatible**: env var names and per-sandbox state file format differ. Sandboxes created by the Python tool need to be re-created with the Go binary — there is no automatic migration.
