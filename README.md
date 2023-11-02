# postgresql_sandbox
A simple-to-use PostgreSQL sandbox helper.

Check `pg_sandbox --help` for detailed usage information and tips.

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

To read more on how to use the different functionality provided by pg_sandbox, you can check the [Demo.md](Demo.md) file.
