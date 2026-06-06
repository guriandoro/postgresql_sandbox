# postgresql_sandbox
A simple-to-use PostgreSQL sandbox helper.

> **Which tool does this README describe?**
> This file documents the **Python** `pg_sandbox` that lives at the repository root and is bundled into `bin/pg_sandbox` by `build/build_executable.sh`. A Go re-implementation lives under [`go/`](./go/); it has its own docs ([`go/README.md`](./go/README.md), [`go/SPEC.md`](./go/SPEC.md), [`go/docs/`](./go/docs/)) and uses different environment-variable names (e.g. `PGS_SANDBOX_ROOT`, not `PGS_ROOT_DIR`) and JSON-based per-sandbox configuration (no `pg_sandbox.env` file). Until the Go port reaches GA, the Python tool described here is the recommended entry point.

Check `pg_sandbox --help` for detailed usage information and tips.

# Standalone executable (macOS / Linux)

You can bundle the Python CLI (`pg_sandbox`, `pg_sandbox_help.py`, `pg_sandbox_errors.py`) into a **single executable** with [PyInstaller](https://pyinstaller.org/). PostgreSQL itself is **not** included; installs under `PGS_BIN_DIR` (or `-b`) are unchanged.

**Requirements**

- Build machine runs **macOS** or **Linux** (`build/build_executable.sh` does not support Windows).
- **Python 3** on `PATH` (`venv` module required for the automatic PyInstaller bootstrap below).
- Run the build **on each OS** (and CPU architecture) you distribute for — the binary is platform-specific.

**Steps**

```bash
chmod +x build/build_executable.sh    # once, if needed
./build/build_executable.sh
```

If `python3` does not already have PyInstaller (common on **Homebrew Python** / **PEP 668** systems where global `pip install` is blocked), the script creates **`build/.venv`**, installs PyInstaller there, and uses it for the bundle. You can still install PyInstaller yourself (`python3 -m pip install pyinstaller` in a venv or with your distro’s packaging) and the script will use that instead.

The output is **`bin/pg_sandbox`**. Use it like the script version, for example `bin/pg_sandbox --help`.

Intermediate PyInstaller files stay under `build/` (`pyinstaller-work/`, optional `pg_sandbox.spec`, and `.venv/` when auto-created). Those paths are listed in `.gitignore`; the binary under `bin/pg_sandbox` is ignored so only `bin/.gitkeep` is tracked.

# Environment Variables

> **Python tool.** The variables below are read by the Python `pg_sandbox`. The Go port under [`go/`](./go/) consumes a different set — see [`go/docs/environment.md`](./go/docs/environment.md). The two most common renamings: `PGS_ROOT_DIR` → `PGS_SANDBOX_ROOT`, and `PGS_ENV_FILE` is dropped (the Go port persists per-sandbox state as JSON).

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
pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18.3
```

Use the sandbox.
```
cd ~/postgresql-sandboxes/pg-18.3/
./use
```

Destroy the sandbox.
```
pg_sandbox destroy -s pg-18.3
```

# Physical replication

`pg_sandbox` can also build streaming replication topologies. Each standby is its own sandbox, attached to an existing one via `pg_basebackup -R`.

Two equivalent entry points:

- Incremental, one node at a time:
  ```
  pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-primary
  pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-s1 \
      --replicate-from pg-18-primary --slot pg_18_s1_slot
  pg_sandbox deploy -b /opt/postgresql/18.3 -s pg-18-s2 \
      --replicate-from pg-18-primary --slot pg_18_s2_slot --sync
  ```
  `--replicate-from` may also point at another standby (cascading).

- One-shot cluster:
  ```
  pg_sandbox cluster deploy  -s rep -b /opt/postgresql/18.3 -N 2 --sync-count 1
  pg_sandbox cluster status  -s rep
  pg_sandbox cluster destroy -s rep -f
  ```
  This creates a per-cluster directory `<PGS_ROOT_DIR>/rep/` containing `rep_p/`, `rep_s1/`, `rep_s2/`, plus a manifest at `<PGS_ROOT_DIR>/rep/cluster.json` used by `cluster status` / `cluster destroy`.

Other replication-related commands:

- `pg_sandbox status -s NAME` also prints `pg_stat_replication` (primaries) or `pg_stat_wal_receiver` + `pg_is_in_recovery()` (standbys) when the instance is running.
- `pg_sandbox promote -s STANDBY` runs `pg_ctl promote` and updates the sandbox env file so subsequent commands treat it as a primary.

The per-sandbox env file (`pg_sandbox.env`) gains optional fields when a sandbox participates in replication: `PGS_ROLE`, `PGS_REPLICATE_FROM`, `PGS_SLOT_NAME`, `PGS_REPL_USER`, `PGS_CLUSTER`. Plain primaries continue to be persisted with the original field set.

# Logical replication

`pg_sandbox` also supports logical replication: a fresh primary subscribes to a publication on another sandbox via `CREATE SUBSCRIPTION`. Unlike physical replication, subscribers are independent primaries (not `pg_basebackup` clones), DDL is not replicated, and a subscription is per-database.

Three entry points cover the common cases:

- Publish on an existing sandbox, then attach a subscriber sandbox:
  ```
  pg_sandbox deploy   -b /opt/postgresql/18.3 -s pg-18-pub
  pg_sandbox publish  -s pg-18-pub --pub-name app_pub --all-tables

  pg_sandbox deploy   -b /opt/postgresql/18.3 -s pg-18-sub \
      --subscribe-to pg-18-pub --pub-name app_pub --copy-schema
  ```
  `--copy-schema` runs `pg_dump --schema-only | psql` from publisher to subscriber before `CREATE SUBSCRIPTION` (logical replication does NOT replicate DDL, so the subscriber needs matching tables before the initial copy can land).

- Subscribe an already-deployed sandbox to a remote publication:
  ```
  pg_sandbox deploy    -b /opt/postgresql/18.3 -s pg-18-sub
  pg_sandbox subscribe -s pg-18-sub --from pg-18-pub --pub-name app_pub --copy-schema
  ```

- One-shot logical cluster (1 publisher + N subscribers, defaults to `FOR ALL TABLES`):
  ```
  pg_sandbox cluster deploy -s lrep -b /opt/postgresql/18.3 -N 2 --logical --copy-schema
  pg_sandbox cluster status -s lrep
  pg_sandbox cluster destroy -s lrep -f
  ```
  This creates `<PGS_ROOT_DIR>/lrep/lrep_p` (publisher), `lrep_s1`, `lrep_s2` (subscribers), and a manifest with `mode: "logical"`, `publication`, `dbname`, `subscriptions`. `cluster destroy` best-effort drops each subscription on the subscriber and the publication on the publisher before tearing members down, so logical slots don't leak.

Notes:

- `--sync` (deploy/subscribe) and `--sync-count` (cluster) work in logical mode the same way they do in physical mode: subscriptions carry `application_name=<sub_name>` so they can match `synchronous_standby_names` on the publisher.
- `pg_sandbox status -s NAME` prints `pg_publication`, `pg_subscription`, and `pg_stat_subscription` whenever they have rows, so the same command surfaces both physical and logical replication state.
- `destroy` best-effort runs `DROP SUBSCRIPTION` against a subscriber sandbox (recorded via `PGS_SUBSCRIPTION_NAME`) before stopping it, mirroring how today's destroy best-effort drops physical slots.
- New optional env keys persisted when a sandbox participates in logical replication: `PGS_SUBSCRIBE_FROM`, `PGS_SUBSCRIPTION_NAME`, `PGS_PUBLICATION_NAME`, `PGS_SUBSCRIPTION_DBNAME`, `PGS_PUBLICATIONS`.

# Demo file with examples

To read more on how to use the different functionality provided by pg_sandbox, you can check the [Demo](DEMO.md) file.
