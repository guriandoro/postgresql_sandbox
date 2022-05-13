# postgresql_sandbox
A simple-to-use PostgreSQL sandbox helper.

Check `pg_sandbox --help` for detailed usage information and tips.

# Basic workflow

```
cd postgresql_sandboxes/
pg_sandbox -b /opt/postgresql/12.6/bin -s ./pg_12.6/ setenv
cd pg_12.6/
pg_sandbox create
pg_sandbox -a run createdb test
pg_sandbox use -d test
pg_sandbox stop
```
```
pg_sandbox start
pg_sandbox use
pg_sandbox destroy
```
