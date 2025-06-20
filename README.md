# postgresql_sandbox
A simple-to-use PostgreSQL sandbox helper.

Check `pg_sandbox --help` for detailed usage information and tips.

# Environment Variables

The following environment variables can be used to customize the behavior of pg_sandbox:

- `PGS_BUILD_DIR`: Sets the directory where PostgreSQL source code is downloaded and compiled during the build process. Defaults to `/tmp/postgresql-sandbox-build/` if not set.

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
