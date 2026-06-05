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
| `PGS_PG_GATHER_DIR` | `pg_gather` scripts location (used by `report`) | (none) |
| `PGS_BUILD_DIR` | Build scratch directory (used by `build`) | `$TMPDIR/pg_sandbox-build/` |
| `PGS_BUILD_DEBUG` | Set to `1` to retain the build scratch tree and surface raw `./configure` / `make` output. Narrow scope — only `build` reads it. | unset |
| `XDG_CONFIG_HOME` | Standard XDG var — controls where the global config file is read from (`$XDG_CONFIG_HOME/pg_sandbox/config.json`) | `$HOME/.config` |

`PGS_BIN_DIR` is the variable you'll set most often — once per shell session — to avoid repeating `--bin-dir /opt/postgresql/.../bin` on every command.

## Variables exported to child processes

When running PostgreSQL utilities via `use`, `run`, etc., `pg_sandbox` sets `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE` in the child environment. This means downstream tools work as expected even when invoked without their own connection flags.

## Planned but not yet wired

These appear in `SPEC.md` §4.9 but the Go port does not currently consume them. Until that wiring lands, setting them has no effect.

| Variable | Planned purpose |
|---|---|
| `PGS_LOG_LEVEL` | `debug` / `info` / `warn` / `error`. The parser exists in `internal/ui/log.go` but the dispatcher does not yet thread it through; logging is currently fixed at `info`. |
| `PGS_CONFIG_FILE` | Override of the global config file path. Today the path is computed from `XDG_CONFIG_HOME` only. |
| `PGS_DEBUG` | Top-level alias for "debug logging + external command tracing". The flag `--debug` provides this today; the env-var alias is not yet wired. For build-specific tracing use `PGS_BUILD_DEBUG`. |
| `NO_COLOR` | Standard "disable ANSI color" var. Consulted by `--color=auto` (the default): when stderr is a TTY and `NO_COLOR` is unset, color would render — but the Go port emits no color output today, so there's nothing to suppress. |
