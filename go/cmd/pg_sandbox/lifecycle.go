// CLI wiring for `start` / `stop` / `restart`. SPEC §6.2.
//
// These three commands share a flag set and a wiring shape, so they
// live in a single file. Each entry point parses --sandbox-dir,
// loads the sandbox config to pick up BinDir (needed to locate
// pg_ctl), and delegates to the matching sandbox.* function.

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

func runStart(args []string, stdout, stderr io.Writer) int {
	return lifecycleCommand("start", args, stdout, stderr, sandbox.Start)
}

func runStop(args []string, stdout, stderr io.Writer) int {
	return lifecycleCommand("stop", args, stdout, stderr, sandbox.Stop)
}

func runRestart(args []string, stdout, stderr io.Writer) int {
	return lifecycleCommand("restart", args, stdout, stderr, sandbox.Restart)
}

// lifecycleCommand is the shared body of start/stop/restart. The op
// parameter is a function with the common signature
// (ctx, runner, dir, stderrW) → error; that matches all three
// sandbox.* lifecycle functions.
func lifecycleCommand(
	name string,
	args []string,
	_ io.Writer,
	stderr io.Writer,
	op func(ctx context.Context, runner pgexec.Runner, dir string, stderrW io.Writer) error,
) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sandboxDir string
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if sandboxDir == "" {
		fmt.Fprintf(stderr, "pg_sandbox %s: --sandbox-dir is required\n", name)
		usageHint(stderr, name)
		return ui.ExitUsage.Int()
	}

	// Load the config up front to obtain BinDir for pgexec. This
	// also enforces SPEC §4.2 (refuse non-sandbox dirs) twice: once
	// here, once again inside the sandbox.* function. The double
	// check is intentional belt-and-braces — we want a clean exit
	// code from the CLI even if the package is later extended to
	// accept non-sandbox dirs in some mode.
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox %s: not a sandbox: %s\n", name, sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox %s: load config: %v\n", name, err)
		return ui.ExitBadConfig.Int()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(cfg.BinDir)
	if err := op(ctx, runner, sandboxDir, stderr); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox %s: %v\n", name, err)
		return sandbox.ExitCodeFor(err).Int()
	}
	return ui.ExitOK.Int()
}
