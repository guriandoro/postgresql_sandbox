## PostgreSQL Sandbox Quick Demo Commands.

Repo at: https://github.com/guriandoro/postgresql_sandbox

## Environment Variables Examples

The following examples show how to use environment variables to customize the PostgreSQL sandbox behavior:

### Custom Root Directory
Instead of using the default `~/postgresql-sandboxes/`, you can set a custom root directory:
```bash
export PGS_ROOT_DIR="/tmp/my-postgres-sandboxes/"
pg_sandbox deploy -b /opt/postgresql/18.3/ -s pg-18.3
# Sandbox will be created in /tmp/my-postgres-sandboxes/pg-18.3/
```

### Custom Binary Directory
If you have PostgreSQL binaries installed in a different location:
```bash
export PGS_BIN_DIR="/usr/local/postgresql/"
pg_sandbox build 18.3
# Binaries will be installed in /usr/local/postgresql/18.3/
```

### Custom Build Directory
For temporary builds, you can use a different directory:
```bash
export PGS_BUILD_DIR="/tmp/my-postgres-builds/"
pg_sandbox build 18.3
# Source code will be downloaded and compiled in /tmp/my-postgres-builds/
```

### Debug Build
To compile PostgreSQL with debug flags for development:
```bash
export PGS_BUILD_DEBUG="1"
pg_sandbox build 18.3
# PostgreSQL will be compiled with --enable-cassert, --enable-debug, and debug CFLAGS
```

### Enable Debug Output
To see detailed debug information during script execution:
```bash
export PGS_DEBUG="1"
pg_sandbox deploy -b /opt/postgresql/18.3/ -s pg-18.3
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
pg_sandbox deploy -b /opt/postgresql/18.3/ -s pg-18.3
# Will create my_sandbox_config.json instead of pg_sandbox.env
```

### Combining Multiple Environment Variables
You can combine multiple environment variables for a fully customized setup:
```bash
export PGS_ROOT_DIR="/opt/sandboxes/"
export PGS_BIN_DIR="/usr/local/postgresql/"
export PGS_BUILD_DIR="/tmp/builds/"
export PGS_DEBUG="1"
pg_sandbox build 18.3
pg_sandbox deploy -b /usr/local/postgresql/18.3/ -s pg-18.3
```

Check help outputs
```
pg_sandbox help
```

Build a new postgres version we don't have. Since PostgreSQL doesn't offer tarball releases, we have to compile it on our own. We are also compiling the contrib packages, so we have the typical extensions (like pg_stat_statements) available to use
```
pg_sandbox build 18.3
```

Deploy our first sandbox
```
pg_sandbox deploy -b /opt/postgresql/18.3/ -s pg-18.3
```

Change dir to postgres sandboxes home (if it wasn't already created, it will prompt to create)
```
cd ~/postgresql-sandboxes/
ls -l
```

Try to create another sandbox with same command (it will generate a port error)
```
pg_sandbox deploy -b /opt/postgresql/18.3/ -s pg-18.3
```

Override default port (but we are still using the same directory, so it will also error out)
```
pg_sandbox deploy -b /opt/postgresql/18.3/ -s pg-18.3 -p 23444
```

Change the sandbox directory used (this command will succeed)
```
pg_sandbox deploy -b /opt/postgresql/18.3/ -s another-pg-18.3 -p 23444
```

Use the first sandbox deployed
```
cd ~/postgresql-sandboxes/pg-18.3
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
pg_sandbox use -s pg-18.3
```

Go back to our sandbox dir
```
cd ~/postgresql-sandboxes/pg-18.3
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
s -l /opt/postgresql/18.3/bin/
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

## Physical replication

Deploy a primary first.
```
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-primary
```

Attach a streaming standby. The source is prepared on demand (wal_level, max_wal_senders, replication role, pg_hba.conf) and a physical replication slot is created via `pg_basebackup -C --slot=...`.
```
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-s1 \
    --replicate-from pg-18-primary --slot pg_18_s1_slot
```

Attach a synchronous standby. `--sync` appends the standby's name to `synchronous_standby_names` on the primary using the FIRST quorum form.
```
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-s2 \
    --replicate-from pg-18-primary --slot pg_18_s2_slot --sync
```

Cascade off an existing standby instead of the primary.
```
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-s1c \
    --replicate-from pg-18-s1 --slot pg_18_s1c_slot
```

Inspect replication state on either side.
```
pg_sandbox status -s pg-18-primary
pg_sandbox status -s pg-18-s1
```

Promote a standby to a standalone primary.
```
pg_sandbox promote -s pg-18-s2
```

## Clusters (one-shot replication topology)

Deploy a cluster with a primary and N standbys, the first K of which are synchronous, all in one command. Members live together under a per-cluster directory `<PGS_ROOT_DIR>/<cluster>/`, named `<cluster>_p`, `<cluster>_s1`, `<cluster>_s2`, ...
```
pg_sandbox cluster deploy -s rep -b /opt/postgresql/18.3 -N 2 --sync-count 1
```

Show consolidated status for every member of the cluster.
```
pg_sandbox cluster status -s rep
```

Destroy the entire cluster (best-effort drops slots on the primary first, then stops + removes standbys, then the primary, then the manifest, then the per-cluster directory).
```
pg_sandbox cluster destroy -s rep -f
```

## Logical replication

Deploy a publisher (a regular primary that will host a publication).
```
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-pub
```

Create a publication on it. The sandbox is bootstrapped for logical replication on demand: `wal_level=logical` (server restart), `replicator` role, `pg_hba.conf` entry. Use `--all-tables` for the easy demo path.
```
pg_sandbox publish -s pg-18-pub --pub-name app_pub --all-tables
```

Or, target a specific table set. Schema-qualified names are allowed.
```
pg_sandbox publish -s pg-18-pub --pub-name orders_pub \
    --tables public.orders,public.line_items
```

Deploy a fresh subscriber sandbox in one shot. `--copy-schema` runs `pg_dump --schema-only | psql` from publisher to subscriber before `CREATE SUBSCRIPTION` so the initial `copy_data` has table definitions to land into.
```
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-sub \
    --subscribe-to pg-18-pub --pub-name app_pub --copy-schema
```

Or, attach an already-deployed sandbox to a publication on another sandbox. Use `--from` for the natural reading order; `--subscribe-to` works as an alias.
```
pg_sandbox deploy    -b /opt/postgresql/18.3 -s pg-18-sub2
pg_sandbox subscribe -s pg-18-sub2 --from pg-18-pub --pub-name app_pub --copy-schema
```

Mark a subscriber as synchronous on the publisher. Subscriptions carry `application_name=<sub_name>` so they match `synchronous_standby_names` the same way physical standbys do.
```
pg_sandbox subscribe -s pg-18-sub2 --from pg-18-pub --pub-name app_pub --sync
```

Skip the initial copy (existing rows on the publisher are not snapshotted; only changes from now on are replicated).
```
pg_sandbox subscribe -s pg-18-sub2 --from pg-18-pub --pub-name app_pub --no-copy-data
```

Inspect logical state on either side. `status` includes `pg_publication`, `pg_subscription`, and `pg_stat_subscription` for every running instance, so the same command works for publishers, subscribers, or sandboxes that are both.
```
pg_sandbox status -s pg-18-pub
pg_sandbox status -s pg-18-sub
```

Tear down a subscriber. `destroy` best-effort runs `DROP SUBSCRIPTION` (recorded via `PGS_SUBSCRIPTION_NAME` in the env file) before stopping the instance so the publisher reclaims the corresponding logical slot cleanly.
```
pg_sandbox destroy -s pg-18-sub -f
```

## Logical replication cluster (one-shot)

Deploy a logical cluster with one publisher and N subscribers. By default the publication is `pgs_pub` `FOR ALL TABLES` in the `postgres` database; override with `--logical-pub-name`, `-d`, and `--tables`.
```
pg_sandbox cluster deploy -s lrep -b /opt/postgresql/18.3 -N 2 --logical --copy-schema
```

Show consolidated status (header includes `mode: logical`, the publication name, and the database; per-member status surfaces `pg_publication`/`pg_subscription`/`pg_stat_subscription` rows).
```
pg_sandbox cluster status -s lrep
```

Tear down the entire cluster. `cluster destroy` best-effort drops each subscription on the subscriber side first (so the publisher reclaims the logical slot), then drops the publication on the publisher, then removes every member directory and the per-cluster parent.
```
pg_sandbox cluster destroy -s lrep -f
```

Demonstrate that `--sync-count` works the same way in logical mode (the first K subscribers go into the publisher's `synchronous_standby_names`).
```
pg_sandbox cluster deploy -s lrep -b /opt/postgresql/18.3 -N 2 --logical --sync-count 1
```
