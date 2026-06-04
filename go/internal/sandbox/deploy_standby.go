// Physical-standby deploy path (SPEC §6.1, the --replicate-from
// branch).
//
// Why a separate file from deploy.go:
//
//   - The standby path is meaningfully longer than the standalone
//     path (it needs source resolution, source-side prep, basebackup,
//     and a different config writeout) and lives behind a different
//     command-line shape. Keeping it in its own file lets reviewers
//     read either flow without skipping past the other.
//
//   - The standalone path stays the "default reading" — deploy.go is
//     unchanged in spirit and tests against it kept passing through
//     the refactor.
//
// Design choices worth flagging:
//
//   - We reuse the caller-supplied Runner for source-side psql /
//     pg_ctl calls rather than constructing a fresh one pointed at
//     the source's BinDir. Two reasons: (a) in practice source and
//     destination use the same PG install, so the destination's
//     BinDir already has every binary we need; (b) constructing a
//     new Runner here would silently bypass test Fakes, making
//     source-side assertions unreliable. If we ever need to support
//     genuinely different installs across source and destination,
//     the right move is a runner-factory option on DeployOptions.
//
//   - The replicator role is created on the source via psql -X. We
//     never embed a password — the sandbox tool's scope is local and
//     SPEC §11 q2 documents trust auth as the default. Adding a
//     password would force a .pgpass dance that no other sandbox
//     command honors.
//
//   - pg_hba.conf is read whole, scanned for an existing
//     "host replication replicator 127.0.0.1/32 trust" line, and the
//     line is appended idempotently if missing. A reload (not
//     restart) applies the change — pg_ctl reload is non-disruptive
//     for hba changes per the Postgres docs.
//
//   - pg_basebackup is invoked with `-R -X stream -C --slot=<name>`.
//     The `-R` flag asks basebackup to write standby.signal and
//     primary_conninfo into postgresql.auto.conf, so the standby is
//     wired up to its source on first start without manual editing.

package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
)

// pgHbaReplicationLine is the canonical pg_hba.conf entry we ensure
// is present on the source so the standby can connect as the
// "replicator" role from 127.0.0.1. SPEC §11 q2: trust auth is the
// documented default for local sandboxes.
const pgHbaReplicationLine = "host replication replicator 127.0.0.1/32 trust"

// deployStandby implements SPEC §6.1's physical-standby code path.
// Called from Deploy when DeployOptions.ReplicateFrom is non-empty.
func deployStandby(ctx context.Context, runner pgexec.Runner, opts DeployOptions, stderrW io.Writer) (*DeployResult, error) {
	if opts.SlotName == "" {
		return nil, wrapExit(ExitUsage,
			fmt.Errorf("sandbox.Deploy: --slot is required when --replicate-from is set"))
	}

	// Step 1: resolve the source sandbox.
	srcDir, err := resolveSourceSandbox(opts.SandboxDir, opts.ReplicateFrom)
	if err != nil {
		return nil, err
	}
	srcCfg, err := config.LoadSandbox(srcDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: load source config: %w", err)
	}

	// Step 2: source must be running. We check pidfile + port-bind,
	// the same compound the Status command uses. A missing pidfile
	// or a non-listening port means we can't safely run pg_basebackup
	// against it.
	if !isRunning(srcCfg) || !isPortListening(srcCfg) {
		return nil, wrapExit(ExitSourceUnreachable,
			fmt.Errorf("source sandbox %q is not running (port %d on %s)",
				srcCfg.Name, srcCfg.Port, srcCfg.Host))
	}

	// Source-side runner: we reuse the caller's Runner rather than
	// constructing a new pgexec.Exec pointed at srcCfg.BinDir. Two
	// reasons:
	//
	//   - In practice the source and destination use the same PG
	//     install, so the destination's BinDir already has psql /
	//     pg_ctl on it.
	//
	//   - When the caller is a Fake (tests), the SAME fake must
	//     intercept source-side calls or assertions become brittle.
	//     Building a fresh pgexec.Exec here would silently bypass
	//     the test's Fake.
	//
	// If we ever need to support genuinely different installs
	// across source and destination, the right move is to take a
	// runner-factory option on DeployOptions rather than rebuild
	// here.
	srcRunner := runner

	// Step 3: ensure replicator role exists on the source.
	fmt.Fprintf(stderrW, "level=INFO msg=%q source=%q\n",
		"ensuring replicator role on source", srcCfg.Name)
	if err := ensureReplicatorRole(ctx, srcRunner, srcCfg, stderrW); err != nil {
		return nil, err
	}

	// Step 4: ensure pg_hba.conf has a "trust" line for replicator
	// from 127.0.0.1, reload the source so the change takes effect.
	if err := ensureReplicationHba(ctx, srcRunner, srcCfg, stderrW); err != nil {
		return nil, err
	}

	// Step 5: create the destination dir (after source-side prep so
	// failures up to here don't leave an empty stub behind).
	if err := os.MkdirAll(opts.SandboxDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: mkdir %s: %w", opts.SandboxDir, err)
	}

	// Step 6: allocate the standby's port. Same policy as standalone.
	port, err := resolvePort(opts)
	if err != nil {
		return nil, err
	}
	opts.Port = port

	dataDir := filepath.Join(opts.SandboxDir, opts.DataDirName)
	logFile := filepath.Join(opts.SandboxDir, opts.LogName)

	// Step 7: pg_basebackup. `-R` writes standby.signal +
	// primary_conninfo so the standby connects to the source on
	// first start. `-C --slot` creates the physical slot on the
	// source so the standby's WAL receipt is durably tracked.
	fmt.Fprintf(stderrW, "level=INFO msg=%q dest=%q slot=%q source=%q\n",
		"pg_basebackup starting", dataDir, opts.SlotName, srcCfg.Name)
	res := runner.Run(ctx, "pg_basebackup",
		"-D", dataDir,
		"-R",
		"-X", "stream",
		"-C",
		"--slot="+opts.SlotName,
		"-h", srcCfg.Host,
		"-p", fmt.Sprintf("%d", srcCfg.Port),
		"-U", "replicator",
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "pg_basebackup", res.Stderr)
		return nil, wrapExit(ExitBasebackupFailed,
			fmt.Errorf("pg_basebackup exit=%d: %w", res.ExitCode, res.Err))
	}

	// Step 8: start the standby on its own host/port. Same -o trick
	// as standalone deploy / Start (see lifecycle.go for why -o
	// must be re-passed on every start).
	pgctlOpts := fmt.Sprintf("-h %s -p %d", opts.Host, opts.Port)
	startRes := runner.Run(ctx, "pg_ctl",
		"start",
		"-D", dataDir,
		"-l", logFile,
		"-o", pgctlOpts,
		"-w",
	)
	if startRes.Err != nil || startRes.ExitCode != 0 {
		emitStderr(stderrW, "pg_ctl start", startRes.Stderr)
		return nil, wrapExit(ExitPgctlFailed,
			fmt.Errorf("pg_ctl start exit=%d: %w", startRes.ExitCode, startRes.Err))
	}

	// Step 9: write the standby's config. Superuser stays whatever
	// the source uses — pg_basebackup copies the source's catalog
	// including the role list, so the standby has the same superuser
	// the source did, regardless of what the standalone defaults
	// would have picked.
	cfg := config.Defaults()
	cfg.Name = filepath.Base(opts.SandboxDir)
	cfg.BinDir = opts.BinDir
	cfg.DataDir = dataDir
	cfg.LogFile = logFile
	cfg.Host = opts.Host
	cfg.Port = opts.Port
	cfg.Superuser = srcCfg.Superuser
	cfg.DefaultDatabase = opts.Dbname
	cfg.Role = config.RoleStandby
	cfg.CreatedAt = time.Now().UTC()
	cfg.Physical = &config.Physical{
		// We keep SourceSandbox as the user-supplied name (rather
		// than the resolved absolute path) so the file stays
		// portable across machine layouts and reads naturally in
		// `config show`.
		SourceSandbox:   opts.ReplicateFrom,
		SlotName:        opts.SlotName,
		ReplicationUser: "replicator",
		SyncMode:        config.SyncNone,
		AppName:         cfg.Name,
	}

	if err := config.Validate(&cfg); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: validate: %w", err)
	}
	if err := config.SaveSandbox(opts.SandboxDir, &cfg); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: save config: %w", err)
	}

	// Step 10: convenience scripts. Same as standalone — the script
	// set is role-agnostic.
	selfPath := opts.SelfPath
	if selfPath == "" {
		p, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("sandbox.Deploy: resolve self path for scripts: %w", err)
		}
		selfPath = p
	}
	if err := writeConvenienceScripts(opts.SandboxDir, selfPath); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: scripts: %w", err)
	}

	fmt.Fprintf(stderrW, "level=INFO msg=%q name=%q host=%q port=%d source=%q slot=%q\n",
		"deployed standby", cfg.Name, cfg.Host, cfg.Port, opts.ReplicateFrom, opts.SlotName)

	return &DeployResult{
		Sandbox:    &cfg,
		ConnString: connString(cfg),
	}, nil
}

// ensureReplicatorRole creates the "replicator" role on the source
// if it isn't already there. The probe and create both go through
// psql with -X -A -t so the output is deterministic regardless of
// the source operator's psqlrc.
func ensureReplicatorRole(ctx context.Context, srcRunner pgexec.Runner, srcCfg *config.Sandbox, stderrW io.Writer) error {
	probe := srcRunner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", srcCfg.Host,
		"-p", fmt.Sprintf("%d", srcCfg.Port),
		"-U", srcCfg.Superuser,
		"-d", "postgres",
		"-c", "SELECT 1 FROM pg_roles WHERE rolname='replicator';",
	)
	if probe.Err != nil || probe.ExitCode != 0 {
		emitStderr(stderrW, "psql probe replicator", probe.Stderr)
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("probe replicator role: exit=%d: %w", probe.ExitCode, probe.Err))
	}
	if strings.TrimSpace(string(probe.Stdout)) != "" {
		// Already present; idempotent path.
		return nil
	}
	create := srcRunner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", srcCfg.Host,
		"-p", fmt.Sprintf("%d", srcCfg.Port),
		"-U", srcCfg.Superuser,
		"-d", "postgres",
		"-c", "CREATE ROLE replicator REPLICATION LOGIN;",
	)
	if create.Err != nil || create.ExitCode != 0 {
		emitStderr(stderrW, "psql create replicator", create.Stderr)
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("create replicator role: exit=%d: %w", create.ExitCode, create.Err))
	}
	fmt.Fprintf(stderrW, "level=INFO msg=%q source=%q\n",
		"created replicator role on source", srcCfg.Name)
	return nil
}

// ensureReplicationHba appends pgHbaReplicationLine to the source's
// pg_hba.conf if it isn't already there, then reloads the source so
// the change applies. Idempotent: a substring match against the
// canonical line shape is good enough for this slice — operators who
// edit pg_hba by hand and use different formatting will end up with
// a duplicate-but-functional line, which Postgres accepts.
func ensureReplicationHba(ctx context.Context, srcRunner pgexec.Runner, srcCfg *config.Sandbox, stderrW io.Writer) error {
	hbaPath := filepath.Join(srcCfg.DataDir, "pg_hba.conf")
	body, err := os.ReadFile(hbaPath)
	if err != nil {
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("read source pg_hba.conf: %w", err))
	}
	if strings.Contains(string(body), pgHbaReplicationLine) {
		return nil
	}
	// Append with a leading newline guard: if the existing file
	// happens not to end in '\n', our line would join the previous
	// line and become a parse error.
	prefix := ""
	if len(body) > 0 && body[len(body)-1] != '\n' {
		prefix = "\n"
	}
	appended := []byte(prefix + pgHbaReplicationLine + "\n")
	// O_APPEND for atomic concurrent-safe append. Permissions are
	// irrelevant on existing files (we don't pass O_CREATE).
	f, err := os.OpenFile(hbaPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("open source pg_hba.conf for append: %w", err))
	}
	if _, err := f.Write(appended); err != nil {
		_ = f.Close()
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("write source pg_hba.conf: %w", err))
	}
	if err := f.Close(); err != nil {
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("close source pg_hba.conf: %w", err))
	}

	// Reload so the new line takes effect immediately. SIGHUP on
	// postmaster (pg_ctl reload) re-reads pg_hba without bouncing
	// connections.
	reload := srcRunner.Run(ctx, "pg_ctl",
		"reload",
		"-D", srcCfg.DataDir,
	)
	if reload.Err != nil || reload.ExitCode != 0 {
		emitStderr(stderrW, "pg_ctl reload", reload.Stderr)
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("reload source after pg_hba edit: exit=%d: %w",
				reload.ExitCode, reload.Err))
	}
	fmt.Fprintf(stderrW, "level=INFO msg=%q source=%q\n",
		"appended replication line to source pg_hba.conf and reloaded", srcCfg.Name)
	return nil
}
