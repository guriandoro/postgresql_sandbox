## PostgreSQL Sandbox Quick Demo Commands.

Repo at: https://github.com/guriandoro/postgresql_sandbox

## Environment Variables Examples

The following examples show how to use environment variables to customize the PostgreSQL sandbox behavior:

### Custom Root Directory
Instead of using the default `~/postgresql-sandboxes/`, you can set a custom root directory:
```bash
export PGS_ROOT_DIR="/tmp/my-postgres-sandboxes/"
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
# Sandbox will be created in /tmp/my-postgres-sandboxes/pg-15.3/
```

### Custom Binary Directory
If you have PostgreSQL binaries installed in a different location:
```bash
export PGS_BIN_DIR="/usr/local/postgresql/"
pg_sandbox build 15.3
# Binaries will be installed in /usr/local/postgresql/15.3/
```

### Custom Build Directory
For temporary builds, you can use a different directory:
```bash
export PGS_BUILD_DIR="/tmp/my-postgres-builds/"
pg_sandbox build 15.3
# Source code will be downloaded and compiled in /tmp/my-postgres-builds/
```

### Debug Build
To compile PostgreSQL with debug flags for development:
```bash
export PGS_BUILD_DEBUG="1"
pg_sandbox build 15.3
# PostgreSQL will be compiled with --enable-cassert, --enable-debug, and debug CFLAGS
```

### Enable Debug Output
To see detailed debug information during script execution:
```bash
export PGS_DEBUG="1"
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
# Will show debug information about commands being executed
```

### Custom pg_gather Directory
If you have pg_gather scripts in a custom location:
```bash
export PGS_PG_GATHER_DIR="/opt/pg_gather/"
pg_sandbox report out.txt
# Will look for gather_schema.sql and gather_report.sql in /opt/pg_gather/
```

### Custom Environment File Name
To use a different name for the sandbox environment file:
```bash
export PGS_ENV_FILE="my_sandbox_config.json"
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
# Will create my_sandbox_config.json instead of pg_sandbox.env
```

### Combining Multiple Environment Variables
You can combine multiple environment variables for a fully customized setup:
```bash
export PGS_ROOT_DIR="/opt/sandboxes/"
export PGS_BIN_DIR="/usr/local/postgresql/"
export PGS_BUILD_DIR="/tmp/builds/"
export PGS_DEBUG="1"
pg_sandbox build 15.3
pg_sandbox deploy -b /usr/local/postgresql/15.3/ -s pg-15.3
```

Check help outputs
```
pg_sandbox help
```

Build a new postgres version we don't have. Since PostgreSQL doesn't offer tarball releases, we have to compile it on our own. We are also compiling the contrib packages, so we have the typical extensions (like pg_stat_statements) available to use
```
pg_sandbox build 15.3
```

Deploy our first sandbox
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
```

Change dir to postgres sandboxes home (if it wasn't already created, it will prompt to create)
```
cd ~/postgresql-sandboxes/
ls -l
```

Try to create another sandbox with same command (it will generate a port error)
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
```

Override default port (but we are still using the same directory, so it will also error out)
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3 -p 23444
```

Change the sandbox directory used (this command will succeed)
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s another-pg-15.3 -p 23444
```

Use the first sandbox deployed
```
cd ~/postgresql-sandboxes/pg-15.3
```

Check all the scripts that are created (and investigate what they do).
They all have their pg_sandbox command counterpart
```
ls -l
```

Connect to psql
```
./use
```

This is the same as using the following command
```
pg_sandbox use
```

Connect to psql from another directory (we can use the -s argument to tell which sandbox we want to work with)
```
cd
pg_sandbox use -s pg-15.3
```

Go back to our sandbox dir
```
cd ~/postgresql-sandboxes/pg-15.3
ls -l
```

Stop server
```
./stop
```

Start server
```
./start
```

Restart server
```
./restart
```

Check server status
```
./status
```

Insert some data into the instance
```
curl -LO https://raw.githubusercontent.com/guriandoro/postgresql_sandbox/master/insert_simple_test_data.sql
./use < insert_simple_test_data.sql
```

Check which binaries we have in the bin dir, to use with the ./run command
```
s -l /opt/postgresql/15.3/bin/
```

Use pg_dump to create a new dump of the postgres database
```
./run pg_dump postgres > pg_dump_postgres.sql
ls -lh pg_dump_postgres.sql
```
Dump with extra arguments: schema only
```
./run pg_dump -s postgres > pg_dump_s_postgres.sql
```

Dump with extra arguments: data only
```
./run pg_dump -a postgres > pg_dump_a_postgres.sql
ls -l pg_dump*
```
Use the createuser script
```
./run createuser --interactive pmm
```

Destroy sandbox
```
pg_sandbox destroy
```

Destroy sandbox with force flag (this will answer "yes" to all questions)
```
pg_sandbox destroy -f
```

Generate pg_gather report from out.txt file
```
pg_sandbox report out.tx
```

Reply "yes" to all questions when generating the pg_gather report
```
pg_sandbox report -f out.txt
```
