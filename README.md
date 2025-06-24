# postgresql_sandbox
A simple-to-use PostgreSQL sandbox helper.

Check `pg_sandbox --help` for detailed usage information and tips.

# Environment Variables

The following environment variables can be used to customize the behavior of pg_sandbox:

- `PGS_ROOT_DIR`: Sets the root directory where all PostgreSQL sandboxes are stored. Defaults to `~/postgresql-sandboxes/` if not set.
- `PGS_BIN_DIR`: Sets the directory where PostgreSQL binaries are installed. Defaults to `/opt/postgresql/` if not set.
- `PGS_ENV_FILE`: Sets the name of the environment file used to store sandbox configuration. Defaults to `pg_sandbox.env` if not set.
- `PGS_PG_GATHER_DIR`: Sets the directory where pg_gather scripts are located. Defaults to `~/src/support-snippets/postgresql/pg_gather/` if not set.
- `PGS_BUILD_DIR`: Sets the directory where PostgreSQL source code is downloaded and compiled during the build process. Defaults to `/tmp/postgresql-sandbox-build/` if not set.
- `PGS_BUILD_DEBUG`: When set to "1", enables debug flags during PostgreSQL compilation (--enable-cassert, --enable-debug, and debug CFLAGS).
- `PGS_DEBUG`: When set, enables debug output throughout the pg_sandbox script execution.

# Basic workflow

Deploy a sandbox.
```
pg_sandbox deploy -b /opt/postgresql/16.0 -s pg-16.0
```

Use the sandbox.
```
cd ~/postgresql-sandboxes/pg-16.0/
./use
```

Destroy the sandbox.
```
pg_sandbox destroy -s pg-16.0
```

# Demo file with examples

To read more on how to use the different functionality provided by pg_sandbox, you can check the [Demo](DEMO.md) file.
