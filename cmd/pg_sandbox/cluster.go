// CLI wiring for `pg_sandbox cluster`. SPEC §6.11.
//
// `cluster` is a meta-command with three sub-subcommands:
//
//   deploy   — provision a primary + N standbys (or publisher + N
//              subscribers in logical mode).
//   status   — consolidated state across all members.
//   destroy  — tear down all members in reverse order.
//
// Design notes:
//
//   - The dispatcher in runCluster deliberately mirrors the
//     `config` sub-dispatcher (cmd/pg_sandbox/config.go): read
//     args[0], hand off args[1:]. One pattern, learned once.
//
//   - Each sub-subcommand owns its FlagSet and exit-code path.
//
//   - Per SPEC §4.6, diagnostics go to stderr; the conn-string list
//     after deploy and the status output (text or JSON) go to stdout.

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/internal/cluster"
	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runCluster is the dispatcher for `cluster`. Pattern matches the
// top-level dispatcher in main.go and the `config` sub-dispatcher.
// Global flags (--debug / --quiet / --color) that landed at the head
// of args are captured and re-prepended onto args[1:] so the leaf
// FlagSet sees them in the position it expects.
func runCluster(args []string, stdout, stderr io.Writer) int {
	leading, args := captureGlobalFlags(args)
	if len(args) == 0 {
		printClusterUsage(stderr)
		return ui.ExitUsage.Int()
	}
	sub := args[0]
	rest := args[1:]
	if len(leading) > 0 {
		rest = append(append([]string{}, leading...), rest...)
	}
	switch sub {
	case "deploy":
		return runClusterDeploy(rest, stdout, stderr)
	case "status":
		return runClusterStatus(rest, stdout, stderr)
	case "destroy":
		return runClusterDestroy(rest, stdout, stderr)
	case "--help", "-h", "help":
		printClusterUsage(stdout)
		return ui.ExitOK.Int()
	default:
		fmt.Fprintf(stderr, "pg_sandbox cluster: unknown subcommand %q\n", sub)
		printClusterUsage(stderr)
		return ui.ExitUsage.Int()
	}
}

// printClusterUsage writes the `cluster` help text. Used both for the
// no-args / unknown-subcommand error paths (to stderr) and for
// `cluster --help` (to stdout).
func printClusterUsage(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox cluster — manage a named group of sandboxes (SPEC §6.11)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox cluster deploy  -s <cluster-dir> -b <bin-dir> -N <n> [--logical]")
	fmt.Fprintln(w, "                             [--host <addr>] [--port <n>] [--user <name>] [--dbname <name>]")
	fmt.Fprintln(w, "                             [--slot-prefix <pfx>] [--logical-pub-name <name>]")
	fmt.Fprintln(w, "                             [--sync-count <n>] [--init-sql <file>]")
	fmt.Fprintln(w, "  pg_sandbox cluster status  -s <cluster-dir> [--json]")
	fmt.Fprintln(w, "  pg_sandbox cluster destroy -s <cluster-dir> [--force]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Notes:")
	fmt.Fprintln(w, "  --init-sql runs the file against the primary/publisher AFTER it starts (and AFTER")
	fmt.Fprintln(w, "  the publication is created in --logical mode). In --logical mode it also auto-enables")
	fmt.Fprintln(w, "  --copy-schema on every subscriber so initial tables+data replicate end-to-end.")
	fmt.Fprintln(w, "  It handles INITIAL schema only; for schema changes after deploy, apply DDL to each")
	fmt.Fprintln(w, "  member by hand.")
}

// ---------------------------------------------------------------- //
// cluster deploy
// ---------------------------------------------------------------- //

// runClusterDeploy implements `pg_sandbox cluster deploy`. SPEC §6.11.
func runClusterDeploy(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)

	var (
		clusterDir  string
		binDir      string
		host        string
		port        int
		user        string
		dbname      string
		nodes       int
		slotPrefix  string
		logicalMode bool
		logicalPub  string
		syncCount   int
		initSQLFile string
	)
	fs.StringVar(&clusterDir, "sandbox-dir", "", "Target cluster directory (required)")
	fs.StringVar(&clusterDir, "s", "", "Alias for --sandbox-dir")
	fs.StringVar(&binDir, "bin-dir", "", "PostgreSQL bin/ directory (required)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.IntVar(&nodes, "nodes", 0, "Number of standbys/subscribers (>=1, required)")
	fs.IntVar(&nodes, "N", 0, "Alias for --nodes")
	fs.StringVar(&host, "host", "", "Listen address for the primary (default 127.0.0.1)")
	fs.IntVar(&port, "port", 0, "TCP port for the primary (auto-allocated when omitted)")
	fs.IntVar(&port, "p", 0, "Alias for --port")
	fs.StringVar(&user, "user", "", "PG superuser for all members (default postgres)")
	fs.StringVar(&user, "U", "", "Alias for --user")
	fs.StringVar(&dbname, "dbname", "", "Default database for all members (default postgres)")
	fs.StringVar(&dbname, "d", "", "Alias for --dbname")
	fs.StringVar(&slotPrefix, "slot-prefix", "", "Physical-slot name prefix (default: cluster name)")
	fs.BoolVar(&logicalMode, "logical", false, "Build a logical pub/sub cluster instead of physical streaming")
	fs.StringVar(&logicalPub, "logical-pub-name", "", "Publication name when --logical is set (default pgs_pub)")
	fs.IntVar(&syncCount, "sync-count", 0, "First K members synchronous (deferred; treated as async this slice)")
	fs.StringVar(&initSQLFile, "init-sql", "", "Path to a SQL file run against the primary/publisher after it starts (uses psql -v ON_ERROR_STOP=1). In --logical mode, also enables --copy-schema on subscribers.")

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if clusterDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox cluster deploy: --sandbox-dir is required")
		usageHint(stderr, "cluster")
		return ui.ExitUsage.Int()
	}
	if nodes < 1 {
		fmt.Fprintln(stderr, "pg_sandbox cluster deploy: -N/--nodes must be >= 1")
		return ui.ExitUsage.Int()
	}

	// Same explicit-port detection trick as the per-sandbox deploy
	// uses. flag.FlagSet.Visit walks only flags actually present on
	// the command line.
	portExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" || f.Name == "p" {
			portExplicit = true
		}
	})

	// SPEC §3.1 layered resolution for primary defaults: built-in →
	// env → flag. We borrow the same machinery the per-sandbox deploy
	// uses (config.Defaults + config.ApplyEnv) so PGS_* env vars
	// govern the primary just like a standalone deploy.
	//
	// Runs before the --bin-dir required-check so PGS_BIN_DIR is
	// honored — same reasoning as deploy.go.
	base := config.Defaults()
	base, err := config.ApplyEnv(base, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cluster deploy: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	resolvedBinDir := firstNonEmpty(binDir, base.BinDir)
	if resolvedBinDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox cluster deploy: --bin-dir is required (or set PGS_BIN_DIR)")
		usageHint(stderr, "cluster")
		return ui.ExitUsage.Int()
	}

	selfPath, _ := os.Executable()

	mode := config.ClusterPhysical
	if logicalMode {
		mode = config.ClusterLogical
	}

	opts := cluster.DeployOptions{
		ClusterDir:   clusterDir,
		BinDir:       resolvedBinDir,
		Nodes:        nodes,
		Host:         firstNonEmpty(host, base.Host),
		Port:         portOrEnv(port, portExplicit, base.Port),
		PortExplicit: portExplicit,
		Superuser:    firstNonEmpty(user, base.Superuser),
		Dbname:       firstNonEmpty(dbname, base.DefaultDatabase),
		Mode:         mode,
		SlotPrefix:   slotPrefix,
		PubName:      logicalPub,
		SyncCount:    syncCount,
		SelfPath:     selfPath,
		InitSQLFile:  initSQLFile,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(opts.BinDir).WithLogger(logger)
	manifest, err := cluster.Deploy(ctx, runner, opts, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cluster deploy: %v\n", err)
		return mapClusterExit(err)
	}
	fmt.Fprintf(stderr, "level=INFO msg=%q name=%q members=%d\n",
		"cluster deploy complete", manifest.Name, len(manifest.Members))
	return ui.ExitOK.Int()
}

// ---------------------------------------------------------------- //
// cluster status
// ---------------------------------------------------------------- //

// runClusterStatus implements `pg_sandbox cluster status`. SPEC §6.11.
func runClusterStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var (
		clusterDir string
		asJSON     bool
	)
	fs.StringVar(&clusterDir, "sandbox-dir", "", "Target cluster directory (required)")
	fs.StringVar(&clusterDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&asJSON, "json", false, "Emit machine-readable JSON to stdout")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if clusterDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox cluster status: --sandbox-dir is required")
		usageHint(stderr, "cluster")
		return ui.ExitUsage.Int()
	}
	clusterDir = resolveClusterArg(clusterDir, loadGlobalConfig())
	if !config.IsClusterDir(clusterDir) {
		fmt.Fprintf(stderr, "pg_sandbox cluster status: not a cluster: %s\n", clusterDir)
		return ui.ExitNotACluster.Int()
	}

	// The cluster manifest doesn't carry a bin-dir of its own — every
	// per-member sandbox knows its own BinDir, and sandbox.Status
	// (which uses psql) doesn't actually need the runner to be
	// pre-pointed at a bin-dir because the absolute path comes from
	// pgexec.New's Locate. We resolve to the first member's BinDir
	// for a sensible default; a heterogeneous cluster would still
	// work because each member's psql is invoked via path resolution.
	m, err := config.LoadCluster(clusterDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cluster status: load manifest: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	var runnerBinDir string
	if len(m.Members) > 0 {
		firstDir := clusterDir + string(os.PathSeparator) + m.Members[0].Name
		if cfg, err := config.LoadSandbox(firstDir); err == nil {
			runnerBinDir = cfg.BinDir
		}
	}
	runner := pgexec.New(runnerBinDir).WithLogger(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rep, err := cluster.Status(ctx, runner, cluster.StatusOptions{ClusterDir: clusterDir}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cluster status: %v\n", err)
		return mapClusterExit(err)
	}
	if asJSON {
		if err := rep.RenderJSON(stdout); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox cluster status: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		return ui.ExitOK.Int()
	}
	rep.RenderText(stdout)
	return ui.ExitOK.Int()
}

// ---------------------------------------------------------------- //
// cluster destroy
// ---------------------------------------------------------------- //

// runClusterDestroy implements `pg_sandbox cluster destroy`. SPEC §6.11.
func runClusterDestroy(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster destroy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var (
		clusterDir string
		force      bool
	)
	fs.StringVar(&clusterDir, "sandbox-dir", "", "Target cluster directory (required)")
	fs.StringVar(&clusterDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&force, "force", false, "Skip confirmation prompt")
	fs.BoolVar(&force, "f", false, "Alias for --force")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if clusterDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox cluster destroy: --sandbox-dir is required")
		usageHint(stderr, "cluster")
		return ui.ExitUsage.Int()
	}
	clusterDir = resolveClusterArg(clusterDir, loadGlobalConfig())
	if !config.IsClusterDir(clusterDir) {
		fmt.Fprintf(stderr, "pg_sandbox cluster destroy: not a cluster: %s\n", clusterDir)
		return ui.ExitNotACluster.Int()
	}
	m, err := config.LoadCluster(clusterDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cluster destroy: load manifest: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	if !force {
		// Same TTY logic as `destroy`. The cluster command has its
		// own confirmation copy that names the cluster (not a single
		// sandbox) so the user sees what they're agreeing to.
		if !stdinIsTTY() {
			fmt.Fprintln(stderr, "pg_sandbox cluster destroy: stdin is not a TTY and --force was not set; refusing")
			return ui.ExitNotATTY.Int()
		}
		if !confirmClusterDestroy(m.Name, clusterDir, len(m.Members), os.Stdin, stderr) {
			fmt.Fprintln(stderr, "pg_sandbox cluster destroy: aborted")
			return ui.ExitOK.Int()
		}
	}

	// Same bin-dir-from-first-member trick as cluster status. The
	// cluster manifest doesn't carry a BinDir; per-member configs do.
	var runnerBinDir string
	if len(m.Members) > 0 {
		firstDir := clusterDir + string(os.PathSeparator) + m.Members[0].Name
		if cfg, err := config.LoadSandbox(firstDir); err == nil {
			runnerBinDir = cfg.BinDir
		}
	}
	runner := pgexec.New(runnerBinDir).WithLogger(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cluster.Destroy(ctx, runner, cluster.DestroyOptions{ClusterDir: clusterDir}, stderr); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cluster destroy: %v\n", err)
		return mapClusterExit(err)
	}
	return ui.ExitOK.Int()
}

// confirmClusterDestroy prompts on stderr and reads a single line from
// r. Mirrors confirmDestroy in destroy.go but names "cluster" rather
// than "sandbox" and shows the member count so the user knows the
// blast radius.
func confirmClusterDestroy(name, dir string, members int, r io.Reader, stderr io.Writer) bool {
	fmt.Fprintf(stderr, "destroy cluster %q (%d members) at %s? [y/N]: ", name, members, dir)
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes"
}

// mapClusterExit translates a cluster-package error into a numeric
// exit code. We delegate to cluster.ExitCodeFor for cluster-typed
// errors and fall back to the sandbox-package mapping for errors that
// originated below (e.g. ExitNotASandbox from a member's destroy).
func mapClusterExit(err error) int {
	return cluster.ExitCodeFor(err).Int()
}
