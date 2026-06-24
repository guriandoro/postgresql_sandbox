# Environment variables

Environment variables are one layer in the resolution chain (`SPEC.md` §3.1):

```
CLI flag  >  PGS_* env var  >  per-sandbox config  >  global config  >  built-in default
```

The flag always wins. Setting an env var lets you avoid repeating the same flag across many invocations in one shell session.

## Variables consumed today

| Variable | Purpose | Default |
|---|---|---|
| `PGS_SANDBOX_ROOT` | Where new sandboxes are created by default | `~/postgresql-sandboxes/` |
| `PGS_BIN_DIR` | Default PostgreSQL `bin/` directory; fills both the global `DefaultBinDir` and per-sandbox `BinDir` layers | (none) |
| `PGS_HOST` | Default listen / connect host | `127.0.0.1` |
| `PGS_PORT` | Default port for new sandboxes | `65432` |
| `PGS_USER` | Default PG superuser | `postgres` |
| `PGS_DBNAME` | Default database name | `postgres` |
| `PGS_PG_GATHER_DIR` | `pg_gather` scripts location (used by `report`) | (none; falls back to discovering the scripts in the current dir or on `$PATH`) |
| `PGS_BUILD_DIR` | Build scratch directory (used by `build`) | `$TMPDIR/pg_sandbox-build/` |
| `PGS_BUILD_DEBUG` | Set to `1` to retain the build scratch tree and surface raw `./configure` / `make` output. Narrow scope — only `build` reads it. | unset |
| `PGS_DEBUG` | Set non-empty to behave as if `--debug` was passed (debug-level logging plus `# exec:` traces for every external command). The flag wins when both are present; `--quiet` always wins over both. | unset |
| `XDG_CONFIG_HOME` | Standard XDG var — controls where the global config file is read from (`$XDG_CONFIG_HOME/pg_sandbox/config.json`) | `$HOME/.config` |

`PGS_BIN_DIR` is the variable you'll set most often — once per shell session — to avoid repeating `--bin-dir /opt/postgresql/.../bin` on every command.

## Variables exported to child processes

When running PostgreSQL utilities via `use`, `run`, etc., `pg_sandbox` sets `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE` in the child environment. This means downstream tools work as expected even when invoked without their own connection flags.

## Not consumed in the Go port

The following variables were considered but are not consumed; setting them has no effect.

| Variable | Why not |
|---|---|
| `PGS_LOG_LEVEL` | The Go port collapses log control onto `--debug` / `--quiet` (and the `PGS_DEBUG` alias). A separate four-value enum adds surface without a real use case — the same outcomes are reachable through the existing flags. |
| `PGS_CONFIG_FILE` | The global config path follows the standard XDG convention via `XDG_CONFIG_HOME`. Use that instead of a tool-specific override. |
| `NO_COLOR` | Standard "disable ANSI color" var. Consulted by `--color=auto` (the default): when stderr is a TTY and `NO_COLOR` is unset, color would render — but the Go port emits no color output today, so there's nothing to suppress. |
