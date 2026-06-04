// Standalone-sandbox deploy (SPEC §6.1, the path with no
// --replicate-from / --subscribe-to).
//
// Design choices worth flagging:
//
//   - We pass listen-address and port to postgres via pg_ctl's
//     `-o "-h <host> -p <port>"` option rather than rewriting
//     postgresql.conf. This keeps the initdb-produced conf pristine
//     so users who later read it aren't confused about why their
//     ALTER SYSTEM lines disagree with what postgres actually used.
//     The pg_ctl forwarding semantics are documented and stable.
//
//   - initdb is invoked with --auth=trust (SPEC §11 open question 2
//     resolution: the tool's scope is local, all connections come
//     from 127.0.0.1, and the sandbox is explicitly not a security
//     boundary). We log this choice once per deploy at info level
//     so users can't say they weren't told.
//
//   - PortExplicit is a separate field on DeployOptions rather than
//     a sentinel value on Port. Both work; a bool is louder than
//     "port==0 means auto". The CLI layer sets PortExplicit based on
//     whether --port was on argv.
//
//   - The final pg_sandbox.json is written AFTER pg_ctl start
//     returns success. SPEC §6.1 step 5 says "a present config file
//     means fully deployed" — readers can rely on the file's
//     presence as the deploy-completed marker.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/portalloc"
)

// DeployOptions captures every input that influences a standalone
// deploy. The CLI layer in cmd/pg_sandbox/deploy.go populates this
// from flag parsing + env + global config; the sandbox package never
// reads flag.FlagSet directly.
type DeployOptions struct {
	// SandboxDir is the absolute path where the new sandbox will
	// live. Created by Deploy; must not already exist (or must be
	// empty) per SPEC §6.1 failure mode.
	SandboxDir string

	// BinDir is the PostgreSQL bin/ directory. Used to locate
	// initdb, pg_ctl, etc. via pgexec.
	BinDir string

	// Host is the listen address, default 127.0.0.1.
	Host string

	// Port is the TCP port. When PortExplicit is true the value is
	// honored verbatim; when false, Port is the starting point for
	// auto-allocation.
	Port int

	// PortExplicit is true iff the user supplied --port. Controls
	// whether a busy Port becomes ExitPortInUse (true) or triggers
	// auto-allocation (false).
	PortExplicit bool

	// Superuser is the PG superuser name (initdb --username), default
	// postgres.
	Superuser string

	// Dbname is the default database for `use`/`run`, default
	// postgres.
	Dbname string

	// DataDirName is the basename of the data directory under
	// SandboxDir. Default "data". Always interpreted as a child of
	// SandboxDir; an absolute path here would be a misuse and is
	// rejected up-front.
	DataDirName string

	// LogName is the basename of the server log under SandboxDir.
	// Default "server.log".
	LogName string

	// PortBase and PortRange override portalloc defaults for the
	// auto-allocation scan. Zero values fall back to
	// portalloc.DefaultBasePort / DefaultRange.
	PortBase  int
	PortRange int

	// SelfPath is the absolute path of the pg_sandbox binary that's
	// performing this deploy. It gets baked into the convenience
	// scripts so that `./start`/`./stop`/etc. inside the sandbox dir
	// always invoke THIS binary, even when a different `pg_sandbox`
	// (e.g., the legacy Python tool) shadows it on PATH.
	//
	// When empty, Deploy falls back to os.Executable() so callers
	// (the CLI, tests) don't have to set it explicitly. Override
	// at runtime via the PG_SANDBOX_BIN env var when needed.
	SelfPath string

	// ReplicateFrom names the source sandbox this new sandbox should
	// stream-replicate from (physical streaming replication, SPEC
	// §6.1). Empty means a standalone deploy. When non-empty,
	// SlotName is REQUIRED. The string is interpreted via
	// resolveSourceSandbox: absolute path, relative path, or bare
	// sibling name.
	ReplicateFrom string

	// SlotName is the physical replication slot name created on the
	// source via `pg_basebackup -C --slot=…`. Required when
	// ReplicateFrom is set; ignored otherwise.
	SlotName string

	// SubscribeTo names the publisher sandbox this new sandbox
	// should logically subscribe to (SPEC §6.1 logical path). Empty
	// for a standalone or physical-standby deploy. Mutually
	// exclusive with ReplicateFrom — Deploy refuses if both are set.
	SubscribeTo string

	// PubName is the publication on the publisher to attach to.
	// REQUIRED when SubscribeTo is set. Passed through to Subscribe
	// unchanged.
	PubName string

	// SubName is the subscription identifier on this sandbox.
	// Optional; empty means Subscribe defaults to
	// `<this-sandbox-basename>_sub`.
	SubName string

	// CopySchema is the --copy-schema flag for the logical path:
	// run pg_dump --schema-only against the publisher before
	// CREATE SUBSCRIPTION.
	CopySchema bool

	// NoCopyData is the --no-copy-data flag for the logical path:
	// translates to WITH (copy_data = false) on CREATE SUBSCRIPTION.
	NoCopyData bool
}

// DeployResult is what Deploy returns on success: the resolved
// sandbox config and the connection-string the caller prints to
// stdout per SPEC §6.1 step 8.
type DeployResult struct {
	// Sandbox is the fully-populated config that was written to disk.
	Sandbox *config.Sandbox

	// ConnString is the postgresql:// URI suitable for piping into
	// `psql` or libpq-using tools.
	ConnString string
}

// Deploy is SPEC §6.1's entry point. It dispatches between the
// standalone path (no replication) and the physical-standby path
// (`--replicate-from`); see deployStandalone and deployStandby for
// the per-path bodies. Logical replication is a separate slice.
//
// Diagnostic output (info-level summary and warnings) is written via
// stderrW; the connection string is NOT written here — the CLI layer
// chooses where to put it (stdout per SPEC §4.6). Returning the
// connstring in DeployResult keeps this package free of stdout
// policy.
//
// On any documented failure mode, Deploy returns an error wrapping
// one of the exitErr sentinels so the CLI layer can map it to the
// right ui.ExitCode. Wrapping (not equality) is used so callers
// keep useful context for the user.
func Deploy(ctx context.Context, runner pgexec.Runner, opts DeployOptions, stderrW io.Writer) (*DeployResult, error) {
	if err := normalizeDeployOptions(&opts); err != nil {
		return nil, err
	}

	// SPEC §6.1 step 2: refuse to overwrite a non-empty target. We
	// allow an empty existing dir for the convenience of users who
	// pre-create it with specific permissions.
	if err := checkSandboxDirAvailable(opts.SandboxDir); err != nil {
		return nil, err
	}

	// Branch: SPEC §6.1's three code paths. ReplicateFrom non-empty
	// → physical standby; SubscribeTo non-empty → logical subscriber
	// (a standalone deploy followed by Subscribe); otherwise →
	// standalone. The mutual-exclusion check is in
	// normalizeDeployOptions so programmatic callers also see it.
	switch {
	case opts.ReplicateFrom != "":
		return deployStandby(ctx, runner, opts, stderrW)
	case opts.SubscribeTo != "":
		return deploySubscriber(ctx, runner, opts, stderrW)
	default:
		return deployStandalone(ctx, runner, opts, stderrW)
	}
}

// deployStandalone implements SPEC §6.1's standalone path: fresh
// initdb + pg_ctl start, no replication. This is the original Deploy
// body; it's been split out so the dispatcher in Deploy is short and
// the standby path can be added without an N-way branch.
func deployStandalone(ctx context.Context, runner pgexec.Runner, opts DeployOptions, stderrW io.Writer) (*DeployResult, error) {
	// SPEC §6.1 step 3 + §4.3: port allocation.
	port, err := resolvePort(opts)
	if err != nil {
		return nil, err
	}
	opts.Port = port

	// SPEC §6.1 step 4: create the sandbox dir. MkdirAll is fine
	// because checkSandboxDirAvailable already established it's
	// either absent or empty.
	if err := os.MkdirAll(opts.SandboxDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: mkdir %s: %w", opts.SandboxDir, err)
	}

	dataDir := filepath.Join(opts.SandboxDir, opts.DataDirName)
	logFile := filepath.Join(opts.SandboxDir, opts.LogName)

	// SPEC §6.1 step 5: initdb. The flags chosen here are documented
	// in the package doc comment above.
	fmt.Fprintf(stderrW, "level=INFO msg=\"initdb starting\" data=%q user=%q auth=trust\n",
		dataDir, opts.Superuser)
	res := runner.Run(ctx, "initdb",
		"-D", dataDir,
		"--username="+opts.Superuser,
		"--auth=trust",
		"--no-locale",
		"--encoding=UTF8",
	)
	if res.Err != nil || res.ExitCode != 0 {
		// Report captured stderr so the user can see initdb's own
		// diagnostic without hunting a transient log file. We trim
		// trailing whitespace to keep the wrapped error tidy.
		stderrTrimmed := strings.TrimRight(string(res.Stderr), "\n")
		if stderrTrimmed != "" {
			fmt.Fprintf(stderrW, "level=ERROR msg=\"initdb stderr\" output=%q\n", stderrTrimmed)
		}
		return nil, wrapExit(ExitInitdbFailed, fmt.Errorf("initdb exit=%d: %w", res.ExitCode, res.Err))
	}

	// SPEC §6.1 step 7: pg_ctl start. We pass listen address + port
	// via -o so postgresql.conf stays initdb-clean.
	pgctlOpts := fmt.Sprintf("-h %s -p %d", opts.Host, opts.Port)
	startRes := runner.Run(ctx, "pg_ctl",
		"start",
		"-D", dataDir,
		"-l", logFile,
		"-o", pgctlOpts,
		"-w",
	)
	if startRes.Err != nil || startRes.ExitCode != 0 {
		stderrTrimmed := strings.TrimRight(string(startRes.Stderr), "\n")
		if stderrTrimmed != "" {
			fmt.Fprintf(stderrW, "level=ERROR msg=\"pg_ctl stderr\" output=%q\n", stderrTrimmed)
		}
		return nil, wrapExit(ExitPgctlFailed, fmt.Errorf("pg_ctl start exit=%d: %w", startRes.ExitCode, startRes.Err))
	}

	// SPEC §6.1 step 5 (config writeout). We build the Sandbox by
	// starting from Defaults() and overlaying what we know — this
	// matches the same layering pattern the CLI uses for resolution.
	cfg := config.Defaults()
	cfg.Name = filepath.Base(opts.SandboxDir)
	cfg.BinDir = opts.BinDir
	cfg.DataDir = dataDir
	cfg.LogFile = logFile
	cfg.Host = opts.Host
	cfg.Port = opts.Port
	cfg.Superuser = opts.Superuser
	cfg.DefaultDatabase = opts.Dbname
	cfg.Role = config.RolePrimary
	cfg.CreatedAt = time.Now().UTC()

	if err := config.Validate(&cfg); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: validate: %w", err)
	}
	if err := config.SaveSandbox(opts.SandboxDir, &cfg); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: save config: %w", err)
	}

	// SPEC §4.5: emit convenience scripts. Done after config save
	// so that "config file present" is a strict pre-condition for
	// "scripts present", and users who see scripts can rely on a
	// readable config file alongside them.
	selfPath := opts.SelfPath
	if selfPath == "" {
		// Fallback so tests and ad-hoc callers don't need to set it.
		// os.Executable returns an absolute path on every supported
		// OS; symlinks are NOT resolved (we want the path the user
		// invoked us through, not its resolved target).
		p, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("sandbox.Deploy: resolve self path for scripts: %w", err)
		}
		selfPath = p
	}
	if err := writeConvenienceScripts(opts.SandboxDir, selfPath); err != nil {
		return nil, fmt.Errorf("sandbox.Deploy: scripts: %w", err)
	}

	// SPEC §6.1 step 8: success summary to stderr.
	fmt.Fprintf(stderrW, "level=INFO msg=\"deployed sandbox\" name=%q host=%q port=%d\n",
		cfg.Name, cfg.Host, cfg.Port)

	return &DeployResult{
		Sandbox:    &cfg,
		ConnString: connString(cfg),
	}, nil
}

// normalizeDeployOptions fills in defaults for any zero-valued field
// and rejects misuse the caller can't recover from.
func normalizeDeployOptions(opts *DeployOptions) error {
	if opts.SandboxDir == "" {
		return wrapExit(ExitUsage, errors.New("sandbox.Deploy: SandboxDir is required"))
	}
	if !filepath.IsAbs(opts.SandboxDir) {
		// Resolve to absolute up front so the config file (which
		// requires absolute paths) doesn't error later with a
		// confusing message about a basename the user didn't choose.
		abs, err := filepath.Abs(opts.SandboxDir)
		if err != nil {
			return fmt.Errorf("sandbox.Deploy: abs(%s): %w", opts.SandboxDir, err)
		}
		opts.SandboxDir = abs
	}
	if opts.BinDir == "" {
		return wrapExit(ExitUsage, errors.New("sandbox.Deploy: BinDir is required"))
	}
	if !filepath.IsAbs(opts.BinDir) {
		abs, err := filepath.Abs(opts.BinDir)
		if err != nil {
			return fmt.Errorf("sandbox.Deploy: abs(%s): %w", opts.BinDir, err)
		}
		opts.BinDir = abs
	}
	if opts.Host == "" {
		opts.Host = "127.0.0.1"
	}
	if opts.Superuser == "" {
		opts.Superuser = "postgres"
	}
	if opts.Dbname == "" {
		opts.Dbname = "postgres"
	}
	if opts.DataDirName == "" {
		opts.DataDirName = "data"
	}
	if opts.LogName == "" {
		opts.LogName = "server.log"
	}
	if filepath.IsAbs(opts.DataDirName) {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Deploy: DataDirName must be a basename, got absolute %q", opts.DataDirName))
	}
	if filepath.IsAbs(opts.LogName) {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Deploy: LogName must be a basename, got absolute %q", opts.LogName))
	}
	if opts.PortBase <= 0 {
		opts.PortBase = portalloc.DefaultBasePort
	}
	if opts.PortRange <= 0 {
		opts.PortRange = portalloc.DefaultRange
	}
	if opts.Port == 0 && !opts.PortExplicit {
		// Auto-alloc; the value used as the scan base is PortBase.
		opts.Port = opts.PortBase
	}
	// SPEC §6.1: physical and logical replication-on-deploy are
	// mutually exclusive. The brief calls this out explicitly: if
	// both --replicate-from and --subscribe-to land in opts, refuse
	// at usage level. Catching it here ensures programmatic callers
	// see the same error the CLI surfaces.
	if opts.ReplicateFrom != "" && opts.SubscribeTo != "" {
		return wrapExit(ExitUsage, fmt.Errorf(
			"sandbox.Deploy: --replicate-from and --subscribe-to are mutually exclusive"))
	}
	return nil
}

// checkSandboxDirAvailable enforces SPEC §6.1 step 2: an existing
// non-empty directory is a hard error (ExitSandboxExists); an
// existing empty directory is acceptable; a missing directory is
// acceptable.
func checkSandboxDirAvailable(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sandbox.Deploy: stat %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return wrapExit(ExitSandboxExists, fmt.Errorf("sandbox dir %s is not empty", dir))
	}
	return nil
}

// resolvePort implements SPEC §4.3 with the policy split between
// explicit and auto-allocated requests.
func resolvePort(opts DeployOptions) (int, error) {
	if opts.PortExplicit {
		busy, err := portalloc.IsBusy(opts.Host, opts.Port)
		if err != nil && !busy {
			// IsBusy returns (true, err) for genuine errors. The
			// `!busy` guard catches the pathological case where it
			// somehow returned (false, err); treat that as a usage
			// error rather than auto-flipping to a different port.
			return 0, fmt.Errorf("sandbox.Deploy: probe %s:%d: %w", opts.Host, opts.Port, err)
		}
		if busy {
			return 0, wrapExit(ExitPortInUse, fmt.Errorf("port %d on %s is busy", opts.Port, opts.Host))
		}
		return opts.Port, nil
	}
	p, err := portalloc.FreePort(opts.Host, opts.PortBase, opts.PortRange)
	if err != nil {
		return 0, wrapExit(ExitNoFreePort, err)
	}
	return p, nil
}

// connString renders the connection URI for the deployed sandbox.
// SPEC §6.1 step 8 says we print this to stdout; we build it here so
// tests don't depend on stdout-formatting style.
func connString(cfg config.Sandbox) string {
	return fmt.Sprintf("postgresql://%s@%s:%d/%s",
		cfg.Superuser, cfg.Host, cfg.Port, cfg.DefaultDatabase)
}
