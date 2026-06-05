# Examples

> **Note:** Every recipe below runs end-to-end against the Go binary. The Python `pg_sandbox` at the repository root remains the canonical recommendation until the Go port is declared GA, but the Go tool is broadly functional today.
>
> If `which pg_sandbox` resolves to `/usr/local/bin/pg_sandbox` (the legacy Python tool), these examples will hit the Python tool — which uses different flag semantics and will reject the bare names used below with `-s must be a relative path inside the sandbox`. Invoke the Go binary by its full path (e.g. `go/bin/pg_sandbox-darwin-arm64`) or move it ahead on `$PATH` before continuing.

## Session setup

Each recipe below assumes you've exported the install root:

```sh
export PGS_BIN_DIR=/opt/postgresql/18.4
```

Commands that operate on an *existing* sandbox or cluster (`status`, `start`, `stop`, `restart`, `use`, `run`, `destroy`, `promote`, `publish`, `subscribe`, `config show`/`get`/`set`/`validate`, `cluster status`, `cluster destroy`) accept `-s <name>` from any working directory — the name resolves to `<sandboxRoot>/<name>` (default `~/postgresql-sandboxes/<name>`). Absolute paths and `./`-prefixed relative paths are still honored verbatim.

`deploy` and `cluster deploy` use `-s` as the *creation target*: a bare name lands under the current directory. To create new sandboxes under the default root, `cd ~/postgresql-sandboxes/` first.

## Build the version from source

`build` takes the install root via `--bin-dir`; each version lands under `<bin-dir>/<version>/`, so this populates `/opt/postgresql/18.4/` — what the setup snippet above already points at.

```sh
# Compile and install PG 18.4 under /opt/postgresql/18.4/
pg_sandbox build 18.4 --bin-dir /opt/postgresql
```

## A single sandbox

```sh
# Deploy a standalone instance
pg_sandbox deploy -s pg18

# Connect with psql
pg_sandbox use -s pg18

# Run any utility (auto-injected -h/-p/-U/-d)
pg_sandbox run -s pg18 -- pgbench -i

# Status (or status --json for a machine-readable object)
pg_sandbox status -s pg18

# Stop and tear down
pg_sandbox destroy -s pg18 --force
```

## Physical streaming replication

```sh
# Primary
pg_sandbox deploy -s primary

# Async standby attached to it
pg_sandbox deploy -s standby1 \
    --replicate-from primary --slot primary_standby1_slot

# Second async standby (synchronous replication is wired at the cluster
# level via `cluster deploy --sync-count`; that flag is currently
# accepted-but-deferred — see commands.md).
pg_sandbox deploy -s standby2 \
    --replicate-from primary --slot primary_standby2_slot

# Inspect (text output — `status --json` currently emits a stub payload).
pg_sandbox status -s primary

# Promote standby1 if primary dies
pg_sandbox promote -s standby1
```

## Logical pub/sub

```sh
# Publisher
pg_sandbox deploy -s pub
pg_sandbox publish -s pub --pub-name my_pub --all-tables

# Subscriber (creates a fresh sandbox already subscribed)
pg_sandbox deploy -s sub \
    --subscribe-to pub --pub-name my_pub --copy-schema
```

## A managed cluster

```sh
# 1 primary + 2 standbys, first one synchronous
pg_sandbox cluster deploy -s mycluster -N 2 --sync-count 1

# Cluster-wide consolidated status
pg_sandbox cluster status -s mycluster

# Tear down everything in one shot
pg_sandbox cluster destroy -s mycluster --force
```

## Inspecting configuration

```sh
# What is this sandbox actually going to use, and where does each value come from?
pg_sandbox config show -s pg18

# Read a single key
pg_sandbox config get port -s pg18

# Change values atomically (either all apply or none do)
pg_sandbox config set host=0.0.0.0 port=5433 -s pg18

# Convert a Python-era pg_sandbox.env into the new format
pg_sandbox config migrate -s legacy-sandbox
```

## `pg_gather` report

```sh
pg_sandbox report --input /path/to/out.txt --output ./gather.html
```

## Cross-host overview

```sh
pg_sandbox global_status
pg_sandbox global_status --json | jq
```
