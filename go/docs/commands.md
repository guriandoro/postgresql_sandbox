# Commands

This is the user-facing command reference for the Go port. It mirrors the entries in `cmd/pg_sandbox/main.go` and the detailed sections in [`../SPEC.md`](../SPEC.md) — the SPEC remains the authoritative behavior contract; this document is the friendlier per-command summary that `--help` output should match.

See `SPEC.md` §6 for the full RFC-2119 behavior of each command.

## Global flags

Only two flags are recognized *before* the subcommand name; both bypass the subcommand dispatcher entirely:

| Flag | Meaning |
|---|---|
| `--version` / `-V` | Print version + commit + Go runtime, then exit. |
| `--help` / `-h` | Print the top-level command index, then exit. |

## Common per-command flags

These are *not* global — each command parses its own flag set — but they appear with the same name and meaning across the commands that document them. Refer to `pg_sandbox help <command>` (or `SPEC.md` §5) for which command accepts which.

| Flag | Where it applies | Meaning |
|---|---|---|
| `--sandbox-dir <path>` / `-s` | All single-sandbox + cluster commands | Target sandbox (or cluster) directory. |
| `--bin-dir <path>` / `-b` | `deploy`, `build`, `report`, `cleanup-install-versions` | PostgreSQL `bin/` directory. |
| `--host <addr>` | `deploy`, `cluster deploy` | Listen / connect host. |
| `--port <n>` / `-p` | `deploy` | TCP port (auto-allocated when omitted). |
| `--user <name>` / `-U` | `deploy`, `publish`, `subscribe` | PG superuser. |
| `--dbname <name>` / `-d` | `deploy`, `publish`, `subscribe`, `config` | Database name. |
| `--force` / `-f` | `destroy`, `cluster destroy`, `cleanup-install-versions`, `build`, `report` | Skip confirmation prompts. |
| `--json` | `status`, `config show`, `global_status`, `cluster status`, `report` | Machine-readable output. (See per-command notes below — `status --json` is still a stub.) |
| `--global` | `config show` / `get` / `set` / `validate` | Operate on global config instead of the sandbox config. |
| `--root <path>` | `global_status`, `report`, `cleanup-install-versions` | Override the sandbox-root scan path. |

`--debug`, `--quiet`, and `--color` are described in `SPEC.md` §5 but are **not yet wired** in the Go port — passing them today is a usage error.

## Commands

### Lifecycle
- `deploy` — create a new sandbox (optional `--replicate-from` / `--subscribe-to`)
- `destroy` — tear down a sandbox (`--force` to skip prompt)
- `start` / `stop` / `restart` — control PostgreSQL on an existing sandbox
- `status` — report running state + replication info. `--json` is accepted but currently emits a stub payload (`{"todo":"json output deferred to a later slice"}`) and exits OK; the text output is production-ready.
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
- `cluster deploy -N <n> [--sync-count <k>] [--logical] [--init-sql <file>]` — `--sync-count` is accepted but currently treated as async in this slice; the first K members will be reported async until sync wiring lands.
- `cluster status` (`--json` supported)
- `cluster destroy` (`--force` to skip prompt)

### Cross-host & reporting
- `global_status` — list every sandbox on the host
- `report --input out.txt [--output report.html]` — `pg_gather` HTML report

### Source build + maintenance
- `build <version> [--with-icu] [--with-openssl] [--configure-opts=...]` — compile PostgreSQL from source
- `cleanup-install-versions [--force]` — prune unused PG installs

### Meta
- `help [<command>]` — print the top-level command index, or the brief summary for a single command.

## Examples

See [`examples.md`](./examples.md).
