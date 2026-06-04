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

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/portalloc"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runDeploy implements the dispatcher contract for `deploy`.
func runDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		sandboxDir  string
		binDir      string
		host        string
		port        int
		user        string
		dbname      string
		dataDirName string
		logName     string
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

	if err := fs.Parse(args); err != nil {
		// flag already wrote the error to stderr via SetOutput.
		return ui.ExitUsage.Int()
	}
	if sandboxDir == "" || binDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox deploy: --sandbox-dir and --bin-dir are required")
		fs.Usage()
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
	base := config.Defaults()
	base, err := config.ApplyEnv(base, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox deploy: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	opts := sandbox.DeployOptions{
		SandboxDir:   sandboxDir,
		BinDir:       firstNonEmpty(binDir, base.BinDir),
		Host:         firstNonEmpty(host, base.Host),
		Port:         portOrEnv(port, portExplicit, base.Port),
		PortExplicit: portExplicit,
		Superuser:    firstNonEmpty(user, base.Superuser),
		Dbname:       firstNonEmpty(dbname, base.DefaultDatabase),
		DataDirName:  firstNonEmpty(dataDirName, "data"),
		LogName:      firstNonEmpty(logName, "server.log"),
		PortBase:     portalloc.DefaultBasePort,
		PortRange:    portalloc.DefaultRange,
	}

	// SPEC §4.1: Ctrl-C must propagate to child processes. Cancel
	// the context on SIGINT/SIGTERM; pgexec hands the context to
	// exec.CommandContext, which kills the child on cancel.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(opts.BinDir)
	res, err := sandbox.Deploy(ctx, runner, opts, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox deploy: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	// SPEC §6.1 step 8: connection string to stdout.
	fmt.Fprintln(stdout, res.ConnString)
	return ui.ExitOK.Int()
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
