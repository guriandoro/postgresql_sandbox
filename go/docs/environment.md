# Environment variables

Environment variables are one layer in the resolution chain (`SPEC.md` §3.1):

```
CLI flag  >  PGS_* env var  >  per-sandbox config  >  global config  >  built-in default
```

The flag always wins. Setting an env var lets you avoid repeating the same flag across many invocations in one shell session.

## Variables consumed

| Variable | Purpose | Default |
|---|---|---|
| `PGS_SANDBOX_ROOT` | Where new sandboxes are created by default | `~/postgresql-sandboxes/` |
| `PGS_BIN_DIR` | Default PostgreSQL `bin/` directory | (none) |
| `PGS_HOST` | Default listen / connect host | `127.0.0.1` |
| `PGS_PORT` | Default port for new sandboxes | `65432` |
| `PGS_USER` | Default PG superuser | `postgres` |
| `PGS_DBNAME` | Default database name | `postgres` |
| `PGS_LOG_LEVEL` | `debug` / `info` / `warn` / `error` | `info` |
| `PGS_CONFIG_FILE` | Override global config file path | `$XDG_CONFIG_HOME/pg_sandbox/config.json` |
| `PGS_DEBUG` | Set to any non-empty value to enable debug logging (alias for `PGS_LOG_LEVEL=debug` + external command tracing) | unset |
| `PGS_PG_GATHER_DIR` | `pg_gather` scripts location (used by `report`) | (none) |
| `PGS_BUILD_DIR` | Build scratch directory (Phase 2 `build` command) | `$TMPDIR/pg_sandbox-build/` |
| `NO_COLOR` | Standard `NO_COLOR` env — when set (to any value), disables ANSI color output | unset |

## Variables exported to child processes

When running PostgreSQL utilities via `use`, `run`, etc., `pg_sandbox` sets `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE` in the child environment. This means downstream tools work as expected even when invoked without their own connection flags.

## Tips

- `PGS_DEBUG=1 pg_sandbox status -s mybox` prints the full command line of every external process the tool invokes (useful when something fails mysteriously).
- `PGS_BIN_DIR` is the variable you'll set most often — once per shell session — to avoid repeating `--bin-dir /opt/postgresql/.../bin` on every command.
