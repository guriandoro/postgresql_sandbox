// CLI wiring for `pg_sandbox promote`. SPEC §6.8.
//
// promote is a thin wrapper around sandbox.Promote: parse
// --sandbox-dir, load the config for its BinDir, hand off. We do
// the same "non-sandbox dir → ExitNotASandbox" pre-check that the
// other lifecycle commands do so the user sees a clean exit code
// even though sandbox.Promote would refuse independently.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runPromote implements the dispatcher contract for `promote`.
func runPromote(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var sandboxDir string
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if sandboxDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox promote: --sandbox-dir is required")
		usageHint(stderr, "promote")
		return ui.ExitUsage.Int()
	}

	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox promote: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox promote: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
	if err := sandbox.Promote(ctx, runner, sandbox.PromoteOptions{SandboxDir: sandboxDir}, stderr); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox promote: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	return ui.ExitOK.Int()
}

// promoteHelp prints `pg_sandbox help promote`. SPEC §6.8.
func promoteHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox promote — promote a physical standby to primary")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox promote -s <dir>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Runs `pg_ctl promote` against a standby sandbox so it accepts writes. The")
	fmt.Fprintln(w, "sandbox role flips from standby to primary; the upstream sandbox is left as is.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.8.")
}
