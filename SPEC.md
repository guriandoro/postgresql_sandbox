# `pg_sandbox` (Go port) — Functional Specification

> **Status:** Draft 0.1 — covers Phase 1 scope. Phase 2 sections are marked.
> **Authority:** This document is the contract. Implementation PRs must reference SPEC sections (e.g. "implements §6.3 `destroy`"). When implementation reveals a gap or contradiction in this doc, **fix the doc first**, then the code.

---

## 1. Purpose and scope

`pg_sandbox` is a command-line tool that provisions, manages, and tears down PostgreSQL sandbox instances on a single machine, for development, testing, and bug reproduction. It is a **lifecycle orchestrator**, not a database driver, server, or HA system.

Capabilities required of the Go port:

- Stand up an isolated PostgreSQL instance with one command.
- Stand up replicated topologies (physical streaming and logical pub/sub).
- Stand up small clusters (primary + N standbys, or publisher + N subscribers).
- Connect a `psql` client to any managed sandbox.
- Run arbitrary PostgreSQL utilities (`pg_dump`, `pgbench`, etc.) against a managed sandbox without hand-typing connection flags.
- Report the live state of a sandbox (running? primary or standby? lag? subscriptions?).
- List every sandbox on the host.
- Generate a `pg_gather` HTML report from a captured `out.txt`.
- Cleanly tear everything down.

### Out of scope (explicit non-goals)

- High availability, failover orchestration, witness servers, fencing.
- Multi-machine clusters, cloud provisioning.
- PostgreSQL version compatibility shims — the tool calls `initdb`/`pg_ctl`/`psql` from the user-pointed install and trusts that install's own version semantics.
- A long-running daemon. Every invocation is a short-lived process.
- A GUI or TUI. CLI only.
- Windows. macOS and Linux only.

### Phasing

- **Phase 1** (this spec): all runtime and replication commands. The deliverable that makes the tool useful.
- **Phase 2** (sketched in §11): `build` (compile PG from source) and `cleanup-install-versions`. Added after Phase 1 reaches parity.

---

## 2. Conventions used in this document

- **MUST / SHOULD / MAY** carry their RFC 2119 meanings.
- "Sandbox" = a single PostgreSQL instance managed by this tool, living in its own directory.
- "Cluster" = a named group of sandboxes orchestrated together with shared metadata.
- "Sandbox root" = the parent directory under which all sandboxes live (a single host has one).
- Default values in `[brackets]`.
- `<placeholder>` is user-supplied; `LITERAL` is verbatim.
- This doc deliberately avoids prescribing types, package names, or function signatures. Those are implementation choices.

---

## 3. Configuration subsystem (designed fresh)

The configuration subsystem is the most important redesign target in the port. It MUST be designed from first principles, not migrated structurally from the Python `pg_sandbox.env` shell-style file. Goals:

### 3.1 Required properties

1. **One canonical on-disk format** for both per-sandbox state and cluster manifests. The same file extension and parser handle both.
2. **Layered resolution with documented precedence.** When a value is needed, the resolver consults sources in this fixed order, stopping at the first hit:
   1. Explicit CLI flag (e.g. `--port 5433`).
   2. Process environment variable (e.g. `PGS_PORT=5433`).
   3. On-disk sandbox config (the file inside the sandbox dir).
   4. On-disk *global* user config (single file under `$XDG_CONFIG_HOME/pg_sandbox/` or `~/.config/pg_sandbox/`).
   5. Built-in default.
3. **Atomic, validated writes.** Every config mutation MUST be all-or-nothing: write to a temp file in the same directory, fsync, rename over the target. Never leave a half-written config.
4. **Strict schema.** Every key is declared, typed, and documented. Unknown keys are an error on read; the writer never emits them.
5. **Typed values.** Integers stay integers, booleans stay booleans, paths are normalized. No "is the string `"true"` truthy" ambiguity.
6. **Self-describing on disk.** A human opening the file can tell what each key does without external docs. (This may be achieved by JSON-with-comments-stripped-on-read plus a sibling rendered reference, or by JSON Schema with `$schema` and `description` fields — the choice is implementation-level.)
7. **No silent magic state.** Anything that alters behavior MUST be either a flag, a documented env var, or a documented config key. No hidden files in `$HOME` that change runtime behavior.
8. **Inspectability.** A first-class subcommand prints the *effective resolved* configuration for a given sandbox and, for each value, identifies which resolution layer it came from. See §6.7 (`config show`).
9. **Migration path.** A first-class subcommand reads a Python-era `pg_sandbox.env` file and emits the new format alongside it (non-destructively by default; destructive only on explicit flag). See §6.7 (`config migrate`).
10. **Forward-compatible versioning.** The on-disk format carries a `schemaVersion` integer. Readers refuse files with a `schemaVersion` higher than they understand; writers can opt-in to upgrade old files.

### 3.2 The canonical schema (logical view)

The exact field names are decided in implementation. The *logical* contents required for every sandbox are:

- `schemaVersion`
- `name` — unique within its sandbox root
- `binDir` — absolute path to the PostgreSQL `bin/` directory used by this sandbox
- `dataDir` — absolute path to the PostgreSQL data directory (typically inside the sandbox dir)
- `logFile` — absolute path to the server log
- `host` — listen address [127.0.0.1]
- `port` — TCP port
- `superuser` — PG superuser name [postgres]
- `defaultDatabase` — convenience default for `use`/`run` [postgres]
- `role` — one of `primary`, `standby`, `publisher`, `subscriber`, `unknown`
- `cluster` — name of parent cluster if any, else null
- **Physical-replication block** (present only when relevant): source sandbox name, replication slot name, replication user, sync mode (none|sync|potential-sync), application name.
- **Logical-replication block** (present only when relevant): source sandbox name, publication name(s) consumed, subscription name, copy mode, target database.
- `createdAt`, `lastModifiedAt` — RFC 3339 timestamps for audit.

The cluster manifest, stored at the cluster's root, additionally carries:

- `schemaVersion`, `name`, `createdAt`, `lastModifiedAt`
- `mode` — `physical` or `logical`
- `members` — ordered list of `{name, role, syncIndex|null}`
- `replication` — slot prefix or publication name, sync count, etc.

### 3.3 Global config

A single host-wide config file (under `$XDG_CONFIG_HOME/pg_sandbox/config.<ext>`) MAY hold defaults that apply across sandboxes:

- `sandboxRoot` — where new sandboxes are created by default
- `defaultBinDir` — convenience default for `--bin-dir`
- `pgGatherDir` — pg_gather scripts location (§6.13)
- `defaultPortBase`, `defaultPortRange` — port allocation policy

It is OPTIONAL. The tool MUST work with no global config present.

### 3.4 The `config` subcommand (replaces Python `setenv`)

See §6.7. In short: `config show`, `config set KEY VALUE …`, `config get KEY`, `config validate`, `config migrate`.

---

## 4. Cross-cutting requirements

### 4.1 Process model

- Every invocation is a one-shot CLI. Exit code communicates success/failure.
- The tool MAY fork/exec PostgreSQL binaries; it MUST NOT keep them as supervised children. Once `pg_ctl start` returns success, the tool's job is done.
- All long operations MUST be cancellable by `SIGINT`/`SIGTERM`: the tool propagates the signal to child processes and exits with a documented code.

### 4.2 Sandbox detection

A directory is a sandbox iff it contains the canonical per-sandbox config file. The tool MUST refuse to treat any other directory as one.

### 4.3 Port allocation

- If `--port` is supplied explicitly and busy → error, exit with the documented port-in-use code. Do not silently change the port.
- If no `--port` is supplied → start from a documented base [65432] and scan forward up to a documented range [100 ports]. First free port wins. If none free in range → error.
- "Busy" means cannot `bind()` to `127.0.0.1:<port>` (or whatever host is configured). Liveness check, not registry lookup.

### 4.4 Filesystem layout

Per-sandbox directory layout MUST be:

```
<sandbox-dir>/
  pg_sandbox.json                  # canonical sandbox config (§3)
  data/                            # initdb-created PG data dir (path overridable in config)
  server.log                       # PG server log (path overridable in config)
  start, stop, restart, status, use, run    # convenience scripts (§4.5)
```

Per-cluster directory layout MUST be:

```
<cluster-dir>/
  pg_sandbox-cluster.json          # cluster manifest (§3)
  <member-1>/                      # each member is a normal sandbox dir
  <member-2>/
  …
```

### 4.5 Convenience scripts

On `deploy`, the tool MUST emit executable shell scripts in the sandbox dir that wrap the equivalent `pg_sandbox <cmd> --sandbox-dir <this-dir>` invocation. Required scripts: `start`, `stop`, `restart`, `status`, `use`, `run`. They MUST:

- Be POSIX shell, no bash-isms.
- Locate the `pg_sandbox` binary via `PATH` (falling back to a documented env var override).
- Pass all positional args through (so `./run pg_dump postgres` works).
- Have `0755` permissions.

### 4.6 Logging and output

- All diagnostic output goes to **stderr**, never stdout.
- **stdout** is reserved for machine-consumable output: `use`'s passed-through psql output, `run`'s passed-through tool output, `status --json`, `global_status --json`, `config get`, etc.
- Diagnostic output is **structured** (key=value or JSON line, switchable). Default is human-friendly key=value with severity prefix.
- Severities: `debug`, `info`, `warn`, `error`. Default visibility threshold is `info`.
- A `--debug` global flag lowers the threshold to `debug` and additionally prints the *full command line* of every external process the tool invokes, *before* invoking it (with a stable prefix like `# exec: …`). This replaces the Python `PGS_DEBUG=1` behavior.
- A `--quiet` global flag raises the threshold to `error`.
- Color output is OFF by default; opt-in via `--color=auto|always|never`. `auto` enables color only when stderr is a TTY *and* `NO_COLOR` is unset.

### 4.7 Confirmation prompts

- Destructive operations (`destroy`, `cluster destroy`, `cleanup-install-versions`, `config migrate --replace`) MUST prompt for `y/N` confirmation.
- `--force` / `-f` MUST suppress the prompt.
- If stdin is not a TTY and `--force` is not set, the operation MUST refuse with a clear error code rather than silently proceeding or silently aborting.

### 4.8 External binaries the tool depends on

The tool shells out to (and MUST locate via `binDir` first, then `PATH`):

- `initdb`, `pg_ctl`, `psql`, `pg_basebackup`, `pg_dump`, `pg_config` — for sandbox lifecycle and replication.
- `make`, `./configure`, `tar`, `curl` or `wget` — for the `build` command (Phase 2 only).

The tool MUST NOT depend on `bash` features, `sed`, `awk`, `perl`, or other coreutils beyond what POSIX shell offers in the convenience scripts.

### 4.9 Environment variables consumed

| Variable | Purpose | Default |
|---|---|---|
| `PGS_SANDBOX_ROOT` | Where new sandboxes go | `~/postgresql-sandboxes/` |
| `PGS_BIN_DIR` | Default PostgreSQL `bin/` directory | (none) |
| `PGS_HOST` | Default listen host | `127.0.0.1` |
| `PGS_PORT` | Default port for new sandboxes | `65432` |
| `PGS_USER` | Default PG superuser | `postgres` |
| `PGS_DBNAME` | Default database name | `postgres` |
| `PGS_DEBUG` | Set non-empty to behave as if `--debug` was passed | unset |
| `PGS_PG_GATHER_DIR` | pg_gather scripts location | (none) |
| `PGS_BUILD_DIR` | Build scratch dir (Phase 2) | `$TMPDIR/pg_sandbox-build/` |
| `NO_COLOR` | Standard; disables color output if set | unset |

`PGS_LOG_LEVEL` and `PGS_CONFIG_FILE` are deliberately not consumed in the Go port — log control collapses onto `--debug` / `--quiet` / `PGS_DEBUG`, and the global config path follows standard XDG via `XDG_CONFIG_HOME`. See `docs/environment.md` for the full rationale.

CLI flag always wins over env var (§3.1).

---

## 5. Global CLI flags

These MAY be supplied to any command (subcommand parser MUST accept them either before or after the subcommand name):

| Flag | Meaning |
|---|---|
| `--sandbox-dir <path>` / `-s <path>` | Target sandbox (or cluster) directory. See §5.1. |
| `--bin-dir <path>` / `-b <path>` | PostgreSQL `bin/` directory |
| `--host <addr>` | Listen / connect host |
| `--port <n>` / `-p <n>` | TCP port |
| `--user <name>` / `-U <name>` | PG superuser |
| `--dbname <name>` / `-d <name>` | Database name |
| `--force` / `-f` | Skip confirmation prompts |
| `--debug` | Verbose diagnostic + print external commands |
| `--quiet` | Only print errors |
| `--color <when>` | `auto`\|`always`\|`never` |
| `--help` / `-h` | Per-command help |
| `--version` | Print version and exit |
| `--json` | Machine-readable output (for commands that support it: `status`, `global_status`, `cluster status`, `config show`, `config get`) |

### 5.1 `--sandbox-dir` value resolution

The `--sandbox-dir` / `-s` value is resolved as follows:

1. **Empty** — the command fails with `ExitUsage` ("--sandbox-dir is required").
2. **The literal value already points at a sandbox** (contains `pg_sandbox.json`) **or cluster** (contains `pg_sandbox-cluster.json`) — used verbatim. Covers absolute paths, `./`-prefixed relative paths, and the historical "cd into the sandbox-root, then `-s name`" workflow.
3. **The value contains a path separator** (`/`) but does not point at a sandbox/cluster — used verbatim and the command fails with the usual "not a sandbox / not a cluster" error. A path was an explicit user intent; the tool MUST NOT silently rewrite `./missign` to `<sandboxRoot>/missign`.
4. **Bare name** (no separator, literal does not exist as a sandbox/cluster) — joined onto the resolved `sandboxRoot` (§3.3). If `<sandboxRoot>/<name>` is a sandbox (or cluster, for cluster commands), THAT path is used. Otherwise the bare token is used verbatim and the usual "not a sandbox / not a cluster" error fires.

`deploy` and `cluster deploy` are the exception: they treat `--sandbox-dir` as the *creation target* (the path where the new sandbox/cluster will be initialized), so no bare-name lookup is performed — the value lands at `<cwd>/<value>` if relative.

---

## 6. Commands (Phase 1)

For each command this section defines: **purpose · inputs · behavior · output · failure modes**.

### 6.1 `deploy` — create a new sandbox

**Purpose.** Initialize a brand-new PostgreSQL instance, optionally attached as a physical standby or logical subscriber to an existing sandbox.

**Inputs.**

- Required: `--sandbox-dir`, `--bin-dir`.
- Optional flags: `--port`, `--host`, `--user`, `--dbname`, `--data-dir <subpath>` [`data`], `--log <subpath>` [`server.log`].
- Physical-replication: `--replicate-from <source-sandbox>`; with `--slot <name>` (REQUIRED when `--replicate-from` is set); `--sync` (registers this standby as synchronous on the source).
- Logical-replication: `--subscribe-to <source-sandbox>`; with `--pub-name <name>` (REQUIRED); `--sub-name <name>` (default `<this-sandbox-basename>_sub`); `--copy-schema` (`pg_dump --schema-only` from source before subscribing); `--no-copy-data` (`WITH (copy_data = false)`).

**Behavior.**

1. Validate inputs and resolve effective config (§3.1).
2. If sandbox dir already exists and is non-empty → error.
3. Allocate port (§4.3).
4. Create sandbox dir.
5. If standalone or publisher: `initdb` into `data/`; configure `postgresql.conf` for the requested role; `pg_ctl start`; create the convenience scripts; write the canonical config file last (a present config file means "fully deployed").
6. If physical standby: invoke `pg_basebackup -R -X stream -C --slot=<name> -h <src host> -p <src port> -U <repl user>` (replication role auto-created on source if missing); register sync mode on source if `--sync`; start; write convenience scripts and config.
7. If logical subscriber: deploy a fresh primary (recursive step 5), optionally `pg_dump --schema-only` the source into it, then `CREATE SUBSCRIPTION` against the source; record subscription details in config.
8. Print a one-line success summary to stderr; print connection string (`postgresql://...`) to stdout.

**Failure modes.**

- Sandbox dir exists / not empty → exit code §9 `EXIT_SANDBOX_EXISTS`.
- Port busy and explicit → `EXIT_PORT_IN_USE`.
- Port busy and auto-alloc exhausted → `EXIT_NO_FREE_PORT`.
- `initdb` failure → `EXIT_INITDB_FAILED`, server log path included in error.
- Source sandbox not running / not reachable → `EXIT_SOURCE_UNREACHABLE`.
- `pg_basebackup` failure → `EXIT_BASEBACKUP_FAILED`.
- Logical subscription failure → `EXIT_SUBSCRIPTION_FAILED`.

**Idempotency.** Not idempotent. Re-running on a populated sandbox dir fails fast.

### 6.2 `start` / 6.2.1 `stop` / 6.2.2 `restart`

**Purpose.** Lifecycle control of an existing sandbox.

**Inputs.** `--sandbox-dir` (required). No others.

**Behavior.**

- `start` → `pg_ctl start -D <dataDir> -l <logFile>`. If already running, no-op with `info` message and exit 0.
- `stop` → `pg_ctl stop -D <dataDir> -m fast`. If not running, no-op with `info` and exit 0.
- `restart` → stop then start.

**`stop` parent-scan mode.** If `--sandbox-dir` points to a directory that does *not* contain a sandbox config but *does* contain sandbox subdirectories, `stop` recursively stops all child sandboxes (best-effort; reports per-child status; non-zero exit if any child failed to stop).

**Failure modes.** `EXIT_NOT_A_SANDBOX`, `EXIT_PGCTL_FAILED`. Exit code 0 on no-op.

### 6.3 `destroy` — tear down a sandbox

**Purpose.** Stop the instance and delete the sandbox directory.

**Inputs.** `--sandbox-dir` (required); `--force` to skip confirmation.

**Behavior.**

1. Confirm (§4.7) unless `--force`.
2. If running, `pg_ctl stop -m immediate` (don't wait for graceful flush; user asked us to destroy).
3. If this sandbox is part of a cluster: best-effort `DROP SUBSCRIPTION` (for subscribers) or `pg_drop_replication_slot` on the upstream (for standbys). Failure here is logged but does NOT block destroy.
4. `rm -rf` the sandbox directory.
5. If it was a cluster member, update the cluster manifest to remove it.

**Failure modes.** `EXIT_NOT_A_SANDBOX`, `EXIT_DESTROY_FAILED` if the rmdir cannot complete.

### 6.4 `status` — report sandbox state

**Purpose.** Print whether the sandbox is running, its role, and replication state.

**Inputs.** `--sandbox-dir` (required); `--json` for machine-readable output.

**Behavior.** Report:

- Running? (pidfile present + process alive + answering on socket)
- PID, server start time, PostgreSQL version.
- Role: standalone primary, primary with replicas, standby, publisher, subscriber.
- If primary: `pg_stat_replication` summary (one row per connected standby: app name, state, sync state, lag bytes).
- If standby: `pg_stat_wal_receiver` + `pg_is_in_recovery()`; lag versus source if reachable.
- If publisher: list of publications.
- If subscriber: subscription state from `pg_subscription` and `pg_stat_subscription`.

**Output.** Default: human table to stdout. With `--json`: a single JSON object to stdout.

**Failure modes.** `EXIT_NOT_A_SANDBOX`. NOT-running is exit 0 (it's a reported state, not an error).

### 6.5 `use` — open `psql` against the sandbox

**Purpose.** Drop into an interactive `psql` already connected.

**Inputs.** `--sandbox-dir` (required); any further args are forwarded verbatim to `psql`.

**Behavior.** Exec (replace the current process) `psql -h <host> -p <port> -U <user> -d <dbname> <forwarded args>`. The tool MUST `exec` not fork, so signals, exit code, and TTY behave exactly as if the user ran `psql` themselves.

### 6.6 `run` — run any PG utility against the sandbox

**Purpose.** Run `pg_dump`, `pgbench`, `createuser`, etc. with auto-injected connection flags.

**Inputs.** `--sandbox-dir` (required); first non-flag arg is the binary name (looked up under `binDir`, then `PATH`); remaining args are forwarded; `--no-dsn` suppresses auto-injection (for tools that don't take `-h -p -U`).

**Behavior.** Exec `<binDir>/<bin> -h <host> -p <port> -U <user> [-d <dbname>] <forwarded args>` (the `-d` is omitted when the tool doesn't accept it; the heuristic is "the user supplied it themselves or `--no-dsn` is set" — see implementation note). With `--no-dsn`, just exec with the forwarded args and rely on `PG*` env vars (which the tool MUST set: `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE`).

### 6.7 `config` — inspect & mutate sandbox / global config

This replaces the Python `setenv` and is the implementation of §3.

**Subcommands.**

- `config show [--sandbox-dir <path>] [--global] [--json]` — print effective resolved config. For each key, show the *value* and the *source layer* (flag / env / sandbox file / global file / built-in).
- `config get <KEY> [--sandbox-dir <path>] [--global]` — print one value to stdout. Exit non-zero if undefined.
- `config set <KEY>=<VALUE> [<KEY>=<VALUE> …] [--sandbox-dir <path>] [--global]` — atomic, validated, multi-key set. Either all keys apply or none do. Unknown keys → error.
- `config validate [--sandbox-dir <path>] [--global]` — schema-check the on-disk file without modifying it.
- `config migrate --sandbox-dir <path> [--replace]` — read a legacy `pg_sandbox.env` (Python format) and write the new canonical file next to it. Default writes `pg_sandbox.json` and leaves the `.env` untouched. With `--replace`, removes the old file *after* successful validation of the new one.

**Failure modes.** `EXIT_BAD_CONFIG`, `EXIT_CONFIG_KEY_UNKNOWN`, `EXIT_NOT_A_SANDBOX`.

### 6.8 `promote` — turn a standby into a standalone primary

**Purpose.** Promote a physical standby. After promotion the sandbox becomes a standalone primary and its config is updated accordingly.

**Inputs.** `--sandbox-dir` (required).

**Behavior.**

1. Verify the sandbox is running and is a standby (`pg_is_in_recovery() = true`). Otherwise error.
2. `pg_ctl promote -D <dataDir>`.
3. Wait until `pg_is_in_recovery() = false` (bounded retry; documented timeout).
4. Update the sandbox config: clear the physical-replication block, set `role` to `primary`, append a `promotedAt` timestamp.
5. If the sandbox was a cluster member, update the cluster manifest (the member's role becomes `primary`; the cluster's overall topology may now be invalid, which is reported as a warning but not an error).

**Failure modes.** `EXIT_NOT_A_STANDBY`, `EXIT_PROMOTE_FAILED`.

### 6.9 `publish` — create a publication

**Purpose.** Create a logical-replication publication on an existing sandbox.

**Inputs.** `--sandbox-dir` (required); `--pub-name <name>` (required); EITHER `--all-tables` OR `--tables <T1,T2,…>` (mutually exclusive, one required); `--dbname <name>` (defaults to sandbox default).

**Behavior.** Ensure `wal_level >= logical` and `max_replication_slots`/`max_wal_senders` are sufficient; raise via `ALTER SYSTEM` and restart if needed (with a clear message that restart will happen). Then `CREATE PUBLICATION <pub-name> …`. Update sandbox config to record the publication.

**Failure modes.** `EXIT_PUBLICATION_FAILED`.

### 6.10 `subscribe` — create a subscription

**Purpose.** Create a logical subscription on an existing sandbox attached to a publisher.

**Inputs.** `--sandbox-dir` (required); `--from <publisher-sandbox>` (required); `--pub-name <name>` (required); `--sub-name <name>` (default `<this-sandbox-basename>_sub`); `--copy-schema`; `--no-copy-data`; `--dbname <name>`.

**Behavior.** Optionally `pg_dump --schema-only` from publisher's chosen db into this sandbox's chosen db. Then `CREATE SUBSCRIPTION <sub-name> CONNECTION '…' PUBLICATION <pub-name> WITH (…copy_data=...)`. Update sandbox config to record the subscription.

**Failure modes.** `EXIT_SOURCE_UNREACHABLE`, `EXIT_SCHEMA_COPY_FAILED`, `EXIT_SUBSCRIPTION_FAILED`.

### 6.11 `cluster deploy` / `cluster status` / `cluster destroy`

**Purpose.** Manage a named group of related sandboxes as a unit.

**Common inputs.** `--sandbox-dir <cluster-dir>` (required); `--bin-dir <path>` (required for deploy).

**`cluster deploy`.**

Additional inputs:

- `-N <n>` / `--nodes <n>` — number of standbys/subscribers (≥1, required).
- `--sync-count <k>` [0] — first K members will be marked synchronous **once synchronous wiring lands**. In the Go port today the flag is parsed, validated, and emits a warn-level diagnostic line if `k > 0`; every member is deployed async.
- `--slot-prefix <pfx>` [cluster name] — physical slot name prefix.
- `--logical` — build a logical pub/sub cluster instead of physical streaming. With `--logical-pub-name <name>` [pgs_pub].
- All single-sandbox port/host/user/dbname flags apply to the primary/publisher; subsequent members auto-allocate ports.

Behavior:

1. Validate inputs; refuse if cluster dir exists.
2. Create cluster dir + cluster manifest with `mode` set.
3. Deploy member 0 (primary or publisher) via §6.1's logic, scoped under `<cluster-dir>/<cluster-name>_p/`.
4. Deploy members 1..N (standbys or subscribers) via §6.1's logic, named `<cluster-name>_s<n>`, attached to member 0. When synchronous wiring lands, the first `sync-count` members will be marked synchronous on the source; today every member is deployed async regardless of `--sync-count`.
5. Update cluster manifest with all members and replication details.

**`cluster status`.** Like §6.4 `status` but produces a consolidated view of every member and the inter-member replication state. Supports `--json`.

**`cluster destroy`.** For each member (in *reverse* order — subscribers/standbys before primary/publisher), perform a best-effort §6.3 destroy with `--force`. Then remove the cluster manifest, then the cluster dir if empty. Best-effort slot/subscription cleanup at the source happens during each member's destroy.

**Failure modes.** `EXIT_CLUSTER_EXISTS`, `EXIT_CLUSTER_DEPLOY_FAILED`, `EXIT_NOT_A_CLUSTER`, `EXIT_CLUSTER_DESTROY_PARTIAL` (some members survived).

### 6.12 `global_status` — list every sandbox on the host

**Purpose.** Walk the sandbox root and print one row per sandbox.

**Inputs.** `--root <path>` (defaults to `PGS_SANDBOX_ROOT`); `--json`.

**Behavior.** For each sandbox found (any depth — clusters are nested), print: name, running state, role, host:port, cluster name (if any), version. Group cluster members together visually. Cheap to run — MUST NOT do per-sandbox SQL queries beyond what's needed to determine running state.

### 6.13 `report` — generate a `pg_gather` HTML report

**Purpose.** Run a pg_gather analysis: spin up a throwaway sandbox, load the gather schema, ingest a captured `out.txt`, generate the HTML report, then destroy the sandbox.

**Inputs.** `--input <out.txt>` (required); `--output <report.html>` (default: `report.html` in cwd); `--bin-dir <path>` (required, or resolved from global config); `--pg-gather-dir <path>` (required, or resolved from env / global config); `--destroy-on-failure` / `-D` (optional). The command refuses by default rather than auto-downloading the gather scripts: missing inputs are errors, not prompts, so there is no `--force` flag. `--destroy-on-failure` is **not** a prompt-suppressor (there is no prompt): it controls failure cleanup, so it is a separate, explicitly-named flag rather than an overload of `--force`.

**Behavior.** Internally calls §6.1, §6.5 (psql piping `gather_schema.sql`, then `\copy out.txt`, then `gather_report.sql > report.html`), §6.3. The throwaway sandbox uses a temp directory inside the sandbox root; on success, it is destroyed; on failure, it is left in place for debugging and the temp path is printed — unless `--destroy-on-failure` / `-D` is set, in which case it is destroyed on failure too (falling back to leaving it in place, with a warning, if that cleanup itself fails).

**Failure modes.** `EXIT_REPORT_FAILED`, `EXIT_PG_GATHER_DIR_MISSING`.

### 6.14 `help` — built-in help

**Purpose.** Print usage.

**Inputs.** Optional positional: a command name.

**Behavior.** `pg_sandbox help` → top-level command index. `pg_sandbox help deploy` → detailed help for `deploy`. Per-command `--help` MUST produce the same text as `help <command>`.

---

## 7. Commands (Phase 2 — sketched)

### 7.1 `build` (Phase 2)

Compile PostgreSQL from source. Inputs: positional `<version>` (e.g. `18.4`); optional `--with-icu`, `--with-openssl`, `--configure-opts="…"`. Downloads tarball, extracts under build dir, runs `./configure`, `make -j`, `make install`, then `make` + `make install` in `contrib/`. Per-step logs under build dir.

### 7.2 `cleanup-install-versions` (Phase 2)

Prune unused PostgreSQL install dirs under `binDir`. Cross-references with sandboxes that name them. Prompts unless `--force`.

---

## 8. Exit codes

| Code | Symbolic | Meaning |
|---:|---|---|
| 0 | `EXIT_OK` | Success |
| 1 | `EXIT_GENERIC` | Unclassified error (try to avoid) |
| 2 | `EXIT_USAGE` | Bad CLI usage (unknown flag, missing required) |
| 3 | `EXIT_NOT_A_SANDBOX` | Target dir is not a sandbox |
| 4 | `EXIT_NOT_A_CLUSTER` | Target dir is not a cluster |
| 5 | `EXIT_SANDBOX_EXISTS` | Deploy target already populated |
| 6 | `EXIT_CLUSTER_EXISTS` | Cluster deploy target already populated |
| 7 | `EXIT_BAD_CONFIG` | Config file invalid or schema mismatch |
| 8 | `EXIT_CONFIG_KEY_UNKNOWN` | `config set/get` referenced an undeclared key |
| 9 | `EXIT_PORT_IN_USE` | Explicit port busy |
| 10 | `EXIT_NO_FREE_PORT` | Auto-allocation exhausted |
| 11 | `EXIT_INITDB_FAILED` | `initdb` failed |
| 12 | `EXIT_PGCTL_FAILED` | `pg_ctl` failed |
| 13 | `EXIT_BASEBACKUP_FAILED` | `pg_basebackup` failed |
| 14 | `EXIT_SOURCE_UNREACHABLE` | Replication source not reachable |
| 15 | `EXIT_PUBLICATION_FAILED` | `CREATE PUBLICATION` failed |
| 16 | `EXIT_SUBSCRIPTION_FAILED` | `CREATE SUBSCRIPTION` failed |
| 17 | `EXIT_SCHEMA_COPY_FAILED` | `pg_dump --schema-only` failed |
| 18 | `EXIT_NOT_A_STANDBY` | `promote` called on a non-standby |
| 19 | `EXIT_PROMOTE_FAILED` | Promote didn't complete |
| 20 | `EXIT_DESTROY_FAILED` | rm of sandbox dir failed |
| 21 | `EXIT_CLUSTER_DEPLOY_FAILED` | A member failed to deploy |
| 22 | `EXIT_CLUSTER_DESTROY_PARTIAL` | Some members survived destroy |
| 23 | `EXIT_PG_GATHER_DIR_MISSING` | gather scripts dir absent |
| 24 | `EXIT_REPORT_FAILED` | Report generation pipeline failed |
| 25 | `EXIT_PSQL_FAILED` | A `psql` call failed unexpectedly |
| 26 | `EXIT_INTERRUPTED` | Caught SIGINT/SIGTERM mid-operation |
| 27 | `EXIT_NOT_A_TTY` | Confirmation needed but stdin not a TTY and `--force` not set |
| 28 | `EXIT_INIT_SQL_FAILED` | `cluster deploy --init-sql` file failed to apply against the primary/publisher |
| 29 | `EXIT_BUILD_FAILED` | Phase 2: source build failed |

Reserved range 30–63 left for additions. Codes >= 64 are reserved for shell convention (e.g. signals).

---

## 9. Testing requirements

The Go port MUST ship with a test suite. Three tiers:

1. **Unit tests** (mandatory, run on every CI build):
   - Config schema parsing and validation.
   - Layered resolution precedence.
   - Port-allocator behavior.
   - CLI flag parsing.
   - Command-line construction for external binaries (given inputs X, expected argv Y).
   - Cluster manifest read/write round-trip.
2. **Fake-exec component tests** (mandatory): an injectable exec layer so tests can substitute fake `psql` / `pg_ctl` / `pg_basebackup` and assert on full argv + simulated outputs/errors.
3. **Integration smoke tests** (opt-in via `-tags=integration`): run against a real PG install. Cover deploy → status → destroy; physical replication; logical pub/sub. Skipped automatically when `PGS_BIN_DIR` is unset.

Test coverage target for Phase 1: ≥ 70% line coverage on `internal/` packages.

---

## 10. Documentation requirements

In addition to this SPEC, the port MUST maintain:

- `README.md` — overview, install, quickstart.
- `docs/commands.md` — concise per-command reference (the "how do I use this" doc, generated from `pg_sandbox help` output or hand-maintained).
- `docs/environment.md` — every env var with default, precedence note, and example.
- `docs/exit-codes.md` — the table in §8 plus prose for each code.
- `docs/examples.md` — end-to-end recipes (single instance; sync replication; logical pub/sub; cluster).
- Package-level doc comments (`doc.go` in every package).
- Doc comment on every exported identifier per Go convention.

---

## 11. Open questions for the next implementation session

1. **JSON-with-comments or strict JSON?** Self-describing-on-disk goal (§3.1.6) may push us toward JSONC or YAML — but YAML breaks "stdlib only". Default proposal: strict JSON + a generated `docs/config-schema.md`.
2. **Subscription credentials.** Logical replication needs a usable connection string from subscriber to publisher. Decision needed on whether to use `trust` in pg_hba on the publisher (acceptable for a sandbox tool) or a `.pgpass` file. Default proposal: `trust` from `127.0.0.1`, since the entire tool's scope is local.
3. **Concurrent cluster member deploy.** Cluster deploy of N members is currently sequential. Decision: keep sequential (simpler, easier to reason about failures) or parallelize (faster but harder to diagnose). Default proposal: sequential for Phase 1.
4. **Should `run` set `PGPASSWORD`?** The sandbox uses `trust` auth locally so usually no, but consider for future non-trust configurations.

These are tracked here so we don't forget; implementation may resolve them with reasonable defaults.
