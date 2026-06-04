# Examples

> **Note:** Every recipe below runs end-to-end against the Go binary. The Python `pg_sandbox` at the repository root remains the canonical recommendation until the Go port is declared GA, but the Go tool is broadly functional today.

## A single sandbox

```sh
# Make life easier for the session
export PGS_BIN_DIR=/opt/postgresql/16.2

# Deploy a standalone instance
pg_sandbox deploy -s ~/sandboxes/pg16

# Connect with psql
pg_sandbox use -s ~/sandboxes/pg16

# Run any utility (auto-injected -h/-p/-U)
pg_sandbox run -s ~/sandboxes/pg16 pgbench -i postgres

# Status (or status --json for a machine-readable object)
pg_sandbox status -s ~/sandboxes/pg16

# Stop and tear down
pg_sandbox destroy -s ~/sandboxes/pg16 --force
```

## Physical streaming replication

```sh
# Primary
pg_sandbox deploy -s ~/sandboxes/primary

# Async standby attached to it
pg_sandbox deploy -s ~/sandboxes/standby1 \
    --replicate-from primary --slot primary_standby1_slot

# Second async standby (synchronous replication is wired at the cluster
# level via `cluster deploy --sync-count`; that flag is currently
# accepted-but-deferred — see commands.md).
pg_sandbox deploy -s ~/sandboxes/standby2 \
    --replicate-from primary --slot primary_standby2_slot

# Inspect (text output — `status --json` currently emits a stub payload).
pg_sandbox status -s ~/sandboxes/primary

# Promote standby1 if primary dies
pg_sandbox promote -s ~/sandboxes/standby1
```

## Logical pub/sub

```sh
# Publisher
pg_sandbox deploy -s ~/sandboxes/pub
pg_sandbox publish -s ~/sandboxes/pub --pub-name my_pub --all-tables

# Subscriber (creates a fresh sandbox already subscribed)
pg_sandbox deploy -s ~/sandboxes/sub \
    --subscribe-to pub --pub-name my_pub --copy-schema
```

## A managed cluster

```sh
# 1 primary + 2 standbys, first one synchronous
pg_sandbox cluster deploy -s ~/sandboxes/mycluster -N 2 --sync-count 1

# Cluster-wide consolidated status
pg_sandbox cluster status -s ~/sandboxes/mycluster

# Tear down everything in one shot
pg_sandbox cluster destroy -s ~/sandboxes/mycluster --force
```

## Inspecting configuration

```sh
# What is this sandbox actually going to use, and where does each value come from?
pg_sandbox config show -s ~/sandboxes/pg16

# Read a single key
pg_sandbox config get port -s ~/sandboxes/pg16

# Change values atomically (either all apply or none do)
pg_sandbox config set host=0.0.0.0 port=5433 -s ~/sandboxes/pg16

# Convert a Python-era pg_sandbox.env into the new format
pg_sandbox config migrate -s ~/sandboxes/legacy-sandbox
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
