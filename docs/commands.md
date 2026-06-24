# Commands

This is the user-facing command reference for the Go port. It mirrors the entries in `cmd/pg_sandbox/main.go` and the detailed sections in [`../SPEC.md`](../SPEC.md) — the SPEC remains the authoritative behavior contract; this document is the at-a-glance index of what each command does. For full usage, flags, and examples on a single command, run `pg_sandbox help <command>` (or equivalently `pg_sandbox <command> --help`).

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
| `--sandbox-dir <path>` / `-s` | All single-sandbox + cluster commands | Target sandbox (or cluster) directory. Accepts an absolute path, a `./`-prefixed relative path, or — for commands operating on an *existing* sandbox/cluster — a bare name that resolves to `<sandboxRoot>/<name>` (default `~/postgresql-sandboxes/<name>`). `deploy` and `cluster deploy` treat the value as the literal creation target. |
| `--bin-dir <path>` / `-b` | `deploy`, `build`, `report`, `cleanup-install-versions` | PostgreSQL `bin/` directory. For `report`, when unset (and no `PGS_BIN_DIR` / global `defaultBinDir`) it auto-resolves to the latest install under `/opt/postgresql` — existing binaries only, nothing is built. |
| `--host <addr>` | `deploy`, `cluster deploy` | Listen / connect host. |
| `--port <n>` / `-p` | `deploy` | TCP port (auto-allocated when omitted). |
| `--user <name>` / `-U` | `deploy`, `publish`, `subscribe` | PG superuser. |
| `--dbname <name>` / `-d` | `deploy`, `publish`, `subscribe`, `config` | Database name. |
| `--force` / `-f` | `destroy`, `cluster destroy`, `cleanup-install-versions`, `build` | Skip confirmation prompts. |
| `--json` | `status`, `config show`, `global_status`, `cluster status`, `report` | Machine-readable output. |
| `--global` | `config show` / `get` / `set` / `validate` | Operate on global config instead of the sandbox config. |
| `--root <path>` | `global_status`, `report`, `cleanup-install-versions` | Override the sandbox-root scan path. |
| `--destroy-on-failure` / `-D` | `report` | Destroy the throwaway sandbox even if report generation fails (default: keep it for debugging). Not a prompt-suppressor — distinct from `--force`. |
| `--debug` | All commands | Lowers the log threshold to debug and prints a `# exec: …` line for every external process before invoking it. |
| `--quiet` | All commands | Raises the log threshold to error: suppresses INFO/WARN diagnostic lines. Mutually exclusive with `--debug`. |
| `--color <when>` | All commands | `auto` (default), `always`, or `never`. `auto` enables color only when stderr is a TTY and `NO_COLOR` is unset. Currently parsed and validated; no ANSI color is emitted yet. |

`--debug`, `--quiet`, and `--color` MAY appear before OR after the subcommand name (e.g. both `pg_sandbox --debug status -s X` and `pg_sandbox status --debug -s X` work).

## Commands

### Lifecycle
- `deploy` — create a new sandbox (optional `--replicate-from` / `--subscribe-to`)
- `destroy` — tear down a sandbox (`--force` to skip prompt)
- `start` / `stop` / `restart` — control PostgreSQL on an existing sandbox
- `status` — report running state + replication info. `--json` emits the report as a JSON object (shape mirrors the `key=value` text render).
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
- `report --input out.txt [--output report.html] [--destroy-on-failure]` — `pg_gather` HTML report. When `--output` is omitted the HTML is written alongside `--input`, reusing its base name with a `_report.html` suffix (e.g. `.../out.txt` → `.../out_report.html`). When no `--bin-dir` / `PGS_BIN_DIR` / global `defaultBinDir` is supplied, the latest install under `/opt/postgresql` is used automatically (existing binaries only — nothing is built). When no `--pg-gather-dir` / `PGS_PG_GATHER_DIR` / global `pgGatherDir` is supplied, the current directory and each `$PATH` entry are searched for one holding both `gather_schema.sql` and `gather_report.sql`; the first match is used and logged (existing scripts only — nothing is downloaded). The throwaway sandbox is always destroyed on success; on failure it is kept for debugging unless `--destroy-on-failure` / `-D` is given.

### Source build + maintenance
- `build <version> [--with-icu] [--with-openssl] [--configure-opts=...]` — compile PostgreSQL from source
- `cleanup-install-versions [--force]` — prune unused PG installs

### Meta
- `help [<command>]` — print the top-level command index, or the detailed usage / flags / examples for a single command. Equivalent to `pg_sandbox <command> --help` (the two forms are byte-identical).

## Examples

See [`examples.md`](./examples.md).
