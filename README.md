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

# Physical replication

`pg_sandbox` can also build streaming replication topologies. Each standby is its own sandbox, attached to an existing one via `pg_basebackup -R`.

Two equivalent entry points:

- Incremental, one node at a time:
  ```
  pg_sandbox deploy -b /opt/postgresql/16.2 -s pg-16-primary
  pg_sandbox deploy -b /opt/postgresql/16.2 -s pg-16-s1 \
      --replicate-from pg-16-primary --slot pg_16_s1_slot
  pg_sandbox deploy -b /opt/postgresql/16.2 -s pg-16-s2 \
      --replicate-from pg-16-primary --slot pg_16_s2_slot --sync
  ```
  `--replicate-from` may also point at another standby (cascading).

- One-shot cluster:
  ```
  pg_sandbox cluster deploy  -s rep -b /opt/postgresql/16.2 -N 2 --sync-count 1
  pg_sandbox cluster status  -s rep
  pg_sandbox cluster destroy -s rep -f
  ```
  This creates a per-cluster directory `<PGS_ROOT_DIR>/rep/` containing `rep_p/`, `rep_s1/`, `rep_s2/`, plus a manifest at `<PGS_ROOT_DIR>/rep.cluster.json` used by `cluster status` / `cluster destroy`.

Other replication-related commands:

- `pg_sandbox status -s NAME` also prints `pg_stat_replication` (primaries) or `pg_stat_wal_receiver` + `pg_is_in_recovery()` (standbys) when the instance is running.
- `pg_sandbox promote -s STANDBY` runs `pg_ctl promote` and updates the sandbox env file so subsequent commands treat it as a primary.

The per-sandbox env file (`pg_sandbox.env`) gains optional fields when a sandbox participates in replication: `PGS_ROLE`, `PGS_REPLICATE_FROM`, `PGS_SLOT_NAME`, `PGS_REPL_USER`, `PGS_CLUSTER`. Plain primaries continue to be persisted with the original field set.

# Demo file with examples

To read more on how to use the different functionality provided by pg_sandbox, you can check the [Demo](DEMO.md) file.
