# Commands

> **Status:** This is a skeleton. Each command stub here mirrors the entries in `cmd/pg_sandbox/main.go` and the detailed sections in [`../SPEC.md`](../SPEC.md). As real implementations land, this file is the place to capture the user-facing reference — flags, defaults, examples, exit codes — without the SPEC's RFC 2119 strictness.

See `SPEC.md` §6 for the authoritative behavior of each command. This document is the *user-friendly* version that the binary's `--help` output should match.

## Global flags

These may be supplied to any command, before or after the subcommand name. See `SPEC.md` §5 for the full table.

| Flag | Meaning |
|---|---|
| `--sandbox-dir <path>` / `-s` | Target sandbox directory |
| `--bin-dir <path>` / `-b` | PostgreSQL `bin/` directory |
| `--host <addr>` | Listen / connect host |
| `--port <n>` / `-p` | TCP port |
| `--user <name>` / `-U` | PG superuser |
| `--dbname <name>` / `-d` | Database name |
| `--force` / `-f` | Skip confirmation prompts |
| `--debug` | Verbose diagnostic + print external commands |
| `--quiet` | Only print errors |
| `--color <when>` | `auto` / `always` / `never` |
| `--json` | Machine-readable output (where supported) |
| `--version` / `-V` | Print version and exit |
| `--help` / `-h` | Per-command help |

## Phase 1 commands

### Lifecycle
- `deploy` — create a new sandbox (optional `--replicate-from` / `--subscribe-to`)
- `destroy` — tear down a sandbox (`--force` to skip prompt)
- `start` / `stop` / `restart` — control PostgreSQL on an existing sandbox
- `status` — report running state + replication info (`--json` for machine output)
- `use` — open `psql` against a sandbox
- `run <bin> [args]` — run any PG utility with auto-injected connection flags
- `promote` — promote a physical standby to a standalone primary

### Configuration
- `config show [--global] [--json]` — show effective resolved config + source of each value
- `config get <KEY>` / `config set KEY=VAL ...` / `config validate`
- `config migrate` — convert a legacy Python `pg_sandbox.env` to the new format

### Replication
- `publish --pub-name <name> [--all-tables | --tables T1,T2,...]`
- `subscribe --from <publisher> --pub-name <name> [--copy-schema] [--no-copy-data]`

### Cluster orchestration
- `cluster deploy -N <n> [--sync-count <k>] [--logical]`
- `cluster status` (`--json` supported)
- `cluster destroy` (`--force` to skip prompt)

### Cross-host & reporting
- `global_status` — list every sandbox on the host
- `report --input out.txt [--output report.html]` — `pg_gather` HTML report

### Meta
- `help [<command>]`

## Phase 2 commands

- `build <version> [--with-icu] [--with-openssl] [--configure-opts=...]` — compile PostgreSQL from source
- `cleanup-install-versions [--force]` — prune unused PG installs

## Examples

See [`examples.md`](./examples.md).
