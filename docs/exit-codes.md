# Exit codes

`pg_sandbox` returns a documented exit code for every failure class so scripts can distinguish them. The canonical table is in [`../SPEC.md`](../SPEC.md) §8; this document expands each one into prose.

Conventions:
- **0** is success (including documented no-ops, e.g. `start` on an already-running sandbox).
- **1** is the unclassified fallback. We try to avoid returning 1 — every real failure should map to a specific code.
- **2** is reserved for CLI-usage errors (unknown flag, missing required arg). It mirrors `getopt` convention.
- Codes ≥ 64 are reserved for shell signal conventions; we don't use them.

## Reference

| Code | Symbol | When |
|---:|---|---|
| 0 | `EXIT_OK` | Command succeeded. |
| 1 | `EXIT_GENERIC` | Unclassified error — should be rare. Open an issue if you hit it. |
| 2 | `EXIT_USAGE` | You passed an unknown flag, an unknown command, or omitted a required argument. |
| 3 | `EXIT_NOT_A_SANDBOX` | `--sandbox-dir` does not point to a sandbox (no canonical config file inside). |
| 4 | `EXIT_NOT_A_CLUSTER` | `--sandbox-dir` does not point to a cluster (no cluster manifest inside). |
| 5 | `EXIT_SANDBOX_EXISTS` | `deploy` target dir already populated. |
| 6 | `EXIT_CLUSTER_EXISTS` | `cluster deploy` target dir already populated. |
| 7 | `EXIT_BAD_CONFIG` | Config file is malformed or has an unsupported `schemaVersion`. |
| 8 | `EXIT_CONFIG_KEY_UNKNOWN` | `config set` / `config get` named a key that isn't declared in the schema. |
| 9 | `EXIT_PORT_IN_USE` | `--port` was supplied explicitly and the port is busy. (Without explicit `--port`, the tool auto-allocates.) |
| 10 | `EXIT_NO_FREE_PORT` | Auto-allocation walked the whole configured range without finding a free port. |
| 11 | `EXIT_INITDB_FAILED` | `initdb` returned non-zero. Server log path included in the error message. |
| 12 | `EXIT_PGCTL_FAILED` | `pg_ctl` (start/stop/restart/promote) returned non-zero. |
| 13 | `EXIT_BASEBACKUP_FAILED` | `pg_basebackup` failed during physical standby deploy. |
| 14 | `EXIT_SOURCE_UNREACHABLE` | The replication or subscription source sandbox isn't reachable. |
| 15 | `EXIT_PUBLICATION_FAILED` | `CREATE PUBLICATION` errored on the server. |
| 16 | `EXIT_SUBSCRIPTION_FAILED` | `CREATE SUBSCRIPTION` errored on the server. |
| 17 | `EXIT_SCHEMA_COPY_FAILED` | `pg_dump --schema-only` failed during `subscribe --copy-schema`. |
| 18 | `EXIT_NOT_A_STANDBY` | `promote` called on something that isn't a physical standby. |
| 19 | `EXIT_PROMOTE_FAILED` | `pg_ctl promote` was issued but the instance didn't leave recovery within the timeout. |
| 20 | `EXIT_DESTROY_FAILED` | rm of the sandbox dir failed (e.g., permission denied, mountpoint busy). |
| 21 | `EXIT_CLUSTER_DEPLOY_FAILED` | One or more cluster members failed to deploy. Partially-deployed members are left in place for inspection. |
| 22 | `EXIT_CLUSTER_DESTROY_PARTIAL` | One or more cluster members survived destroy. Cluster dir is preserved with the manifest so you can finish manually. |
| 23 | `EXIT_PG_GATHER_DIR_MISSING` | `report` needs the `pg_gather` scripts directory and didn't find it. |
| 24 | `EXIT_REPORT_FAILED` | `report` pipeline failed somewhere after the throwaway sandbox was created. |
| 25 | `EXIT_PSQL_FAILED` | A `psql` invocation failed unexpectedly (server crash, connection drop). |
| 26 | `EXIT_INTERRUPTED` | Tool caught `SIGINT` or `SIGTERM` and is exiting mid-operation. |
| 27 | `EXIT_NOT_A_TTY` | A confirmation prompt was needed, `--force` wasn't set, and stdin isn't a TTY. Re-run with `--force` if you really mean it. |
| 28 | `EXIT_INIT_SQL_FAILED` | `cluster deploy --init-sql` failed to apply the supplied SQL file against the primary/publisher (psql `ON_ERROR_STOP=1` returned non-zero). The cluster dir + partial primary are left on disk for inspection. |
| 29 | `EXIT_BUILD_FAILED` | `build` (source compilation) failed. |
