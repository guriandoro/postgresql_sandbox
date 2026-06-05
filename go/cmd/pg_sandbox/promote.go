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

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runPromote implements the dispatcher contract for `promote`.
func runPromote(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sandboxDir string
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
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

	runner := pgexec.New(cfg.BinDir)
	if err := sandbox.Promote(ctx, runner, sandbox.PromoteOptions{SandboxDir: sandboxDir}, stderr); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox promote: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	return ui.ExitOK.Int()
}
