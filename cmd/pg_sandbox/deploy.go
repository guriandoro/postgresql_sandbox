// CLI wiring for `pg_sandbox deploy`. SPEC §6.1 (standalone path).
//
// The runDeploy function is the bridge between the dispatcher in
// main.go and the sandbox package. Its responsibilities:
//
//   - Parse the deploy flag set.
//   - Resolve defaults (built-in → env → flag) per SPEC §3.1 — this
//     slice does not consult the global config file; that lands
//     with the `config` command.
//   - Detect whether --port was supplied explicitly (needed for the
//     "explicit busy → ExitPortInUse" branch of SPEC §4.3).
//   - Call sandbox.Deploy and map any returned error to the right
//     exit code via sandbox.ExitCodeFor.
//   - Write the connection string to stdout (SPEC §4.6: stdout is
//     reserved for machine-consumable output).

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/portalloc"
	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runDeploy implements the dispatcher contract for `deploy`.
func runDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)

	var (
		sandboxDir    string
		binDir        string
		host          string
		port          int
		user          string
		dbname        string
		dataDirName   string
		logName       string
		replicateFrom string
		slotName      string
		subscribeTo   string
		pubName       string
		subName       string
		copySchema    bool
		noCopyData    bool
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.StringVar(&binDir, "bin-dir", "", "PostgreSQL bin/ directory (required)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&host, "host", "", "Listen address (default 127.0.0.1)")
	fs.IntVar(&port, "port", 0, "TCP port (auto-allocated when omitted)")
	fs.IntVar(&port, "p", 0, "Alias for --port")
	fs.StringVar(&user, "user", "", "PG superuser (default postgres)")
	fs.StringVar(&user, "U", "", "Alias for --user")
	fs.StringVar(&dbname, "dbname", "", "Default database name (default postgres)")
	fs.StringVar(&dbname, "d", "", "Alias for --dbname")
	// SPEC §6.1 names this flag `--data-dir`. The basename-only
	// semantics are documented in the help text rather than in the
	// flag name, matching what the spec promises users.
	fs.StringVar(&dataDirName, "data-dir", "", "Basename of data dir under --sandbox-dir (default \"data\")")
	fs.StringVar(&logName, "log", "", "Basename of server log under --sandbox-dir (default \"server.log\")")
	// SPEC §6.1 physical-replication flags. --slot is REQUIRED when
	// --replicate-from is set; we enforce that in the sandbox package
	// rather than here so the same check guards programmatic callers.
	fs.StringVar(&replicateFrom, "replicate-from", "", "Source sandbox name (or absolute path) to stream-replicate from")
	fs.StringVar(&slotName, "slot", "", "Physical replication slot name (required with --replicate-from)")
	// SPEC §6.1 logical-replication flags. --pub-name is REQUIRED
	// when --subscribe-to is set; we enforce that in the sandbox
	// package rather than here so programmatic callers see the same
	// check.
	fs.StringVar(&subscribeTo, "subscribe-to", "", "Publisher sandbox name (or absolute path) to subscribe to")
	fs.StringVar(&pubName, "pub-name", "", "Publication name on the publisher (required with --subscribe-to)")
	fs.StringVar(&subName, "sub-name", "", "Subscription name (default <this-sandbox-basename>_sub)")
	fs.BoolVar(&copySchema, "copy-schema", false, "pg_dump --schema-only from the publisher before CREATE SUBSCRIPTION")
	fs.BoolVar(&noCopyData, "no-copy-data", false, "Create subscription with WITH (copy_data = false)")

	if err := fs.Parse(args); err != nil {
		// flag already wrote the error to stderr via SetOutput.
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if sandboxDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox deploy: --sandbox-dir is required")
		usageHint(stderr, "deploy")
		return ui.ExitUsage.Int()
	}

	// Detect whether --port was supplied explicitly. flag.FlagSet
	// has no built-in "was this flag set" predicate, so we walk
	// Visit, which only visits flags that appeared on the command
	// line.
	portExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" || f.Name == "p" {
			portExplicit = true
		}
	})

	// SPEC §3.1 layered resolution: start from defaults, overlay
	// env, then flags. We re-use config.ApplyEnv even though we
	// won't persist the intermediate Sandbox — it's the canonical
	// way to apply PGS_* vars.
	//
	// The env overlay HAS to run before the --bin-dir required-check
	// below; otherwise users setting PGS_BIN_DIR (the documented
	// shell-session shortcut) still trip the "--bin-dir is required"
	// error.
	base := config.Defaults()
	base, err := config.ApplyEnv(base, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox deploy: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	resolvedBinDir := firstNonEmpty(binDir, base.BinDir)
	if resolvedBinDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox deploy: --bin-dir is required (or set PGS_BIN_DIR)")
		usageHint(stderr, "deploy")
		return ui.ExitUsage.Int()
	}

	// SelfPath gets baked into the convenience scripts so they
	// invoke the SAME binary that deployed the sandbox — not
	// whatever `pg_sandbox` happens to be on PATH (the legacy
	// Python tool is a common shadow). os.Executable is reliable
	// on macOS / Linux; if it ever fails, Deploy errors before
	// touching the filesystem.
	selfPath, _ := os.Executable() // empty on rare failure → Deploy retries internally

	opts := sandbox.DeployOptions{
		SandboxDir:    sandboxDir,
		BinDir:        resolvedBinDir,
		Host:          firstNonEmpty(host, base.Host),
		Port:          portOrEnv(port, portExplicit, base.Port),
		PortExplicit:  portExplicit,
		Superuser:     firstNonEmpty(user, base.Superuser),
		Dbname:        firstNonEmpty(dbname, base.DefaultDatabase),
		DataDirName:   firstNonEmpty(dataDirName, "data"),
		LogName:       firstNonEmpty(logName, "server.log"),
		PortBase:      portalloc.DefaultBasePort,
		PortRange:     portalloc.DefaultRange,
		SelfPath:      selfPath,
		ReplicateFrom: replicateFrom,
		SlotName:      slotName,
		SubscribeTo:   subscribeTo,
		PubName:       pubName,
		SubName:       subName,
		CopySchema:    copySchema,
		NoCopyData:    noCopyData,
	}

	// SPEC §4.1: Ctrl-C must propagate to child processes. Cancel
	// the context on SIGINT/SIGTERM; pgexec hands the context to
	// exec.CommandContext, which kills the child on cancel.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(opts.BinDir).WithLogger(logger)
	res, err := sandbox.Deploy(ctx, runner, opts, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox deploy: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	// SPEC §6.1 step 8: connection string to stdout.
	fmt.Fprintln(stdout, res.ConnString)
	return ui.ExitOK.Int()
}

// deployHelp prints `pg_sandbox help deploy`. SPEC §6.1.
func deployHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox deploy — create a new sandbox")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox deploy -s <dir> -b <bin-dir> [flags]")
	fmt.Fprintln(w, "  pg_sandbox deploy -s <dir> -b <bin-dir> --replicate-from <src> --slot <name>")
	fmt.Fprintln(w, "  pg_sandbox deploy -s <dir> -b <bin-dir> --subscribe-to <src> --pub-name <name>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Provisions a PostgreSQL sandbox in <dir> using the binaries at <bin-dir>.")
	fmt.Fprintln(w, "Picks an unused TCP port unless --port is set, runs initdb, starts the cluster,")
	fmt.Fprintln(w, "and prints the connection string on stdout. With --replicate-from a physical")
	fmt.Fprintln(w, "streaming replica is created; with --subscribe-to a logical subscriber.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
		{"-b, --bin-dir <dir>", "PostgreSQL bin/ directory (required; or set PGS_BIN_DIR)"},
		{"    --host <addr>", "Listen address (default 127.0.0.1)"},
		{"-p, --port <n>", "TCP port (auto-allocated when omitted)"},
		{"-U, --user <name>", "PG superuser (default postgres)"},
		{"-d, --dbname <name>", "Default database name (default postgres)"},
		{"    --data-dir <name>", "Basename of data dir under --sandbox-dir (default \"data\")"},
		{"    --log <name>", "Basename of server log under --sandbox-dir (default \"server.log\")"},
		{"    --replicate-from <ref>", "Source sandbox name (or absolute path) to stream-replicate from"},
		{"    --slot <name>", "Physical replication slot name (required with --replicate-from)"},
		{"    --subscribe-to <ref>", "Publisher sandbox name (or absolute path) to subscribe to"},
		{"    --pub-name <name>", "Publication name on the publisher (required with --subscribe-to)"},
		{"    --sub-name <name>", "Subscription name (default <this-sandbox-basename>_sub)"},
		{"    --copy-schema", "pg_dump --schema-only from the publisher before CREATE SUBSCRIPTION"},
		{"    --no-copy-data", "Create subscription with WITH (copy_data = false)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Environment:")
	fmt.Fprintln(w, "  PGS_BIN_DIR fills in --bin-dir; PGS_* also supplies defaults for host/port/user/dbname.")
	fmt.Fprintln(w, "  -s accepts a bare name (resolved under sandboxRoot) or an absolute path.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.1 for the full behavior; docs/examples.md for end-to-end recipes.")
}

// firstNonEmpty returns the first non-empty string in args. It's
// the common "flag won? env won? default won?" tiebreaker.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// portOrEnv chooses between an explicit flag port, an env-supplied
// port, and a default. When the flag was explicitly set we always
// use it (so `--port 0` still means "0, please" — admittedly
// nonsense for a TCP port, but the contract is "the flag wins").
// When the flag was not explicitly set, we fall back to whatever
// the env layer produced.
func portOrEnv(flagPort int, flagSet bool, envPort int) int {
	if flagSet {
		return flagPort
	}
	if envPort != 0 {
		return envPort
	}
	return 0
}
