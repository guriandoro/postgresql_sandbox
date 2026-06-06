// CLI wiring for `pg_sandbox status`. SPEC §6.4.
//
// The default render is key=value lines via sandbox.StatusReport's
// RenderText. --json swaps in RenderJSON, which emits a JSON object
// whose shape is defined by the struct tags in
// internal/sandbox/status.go.

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

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var (
		sandboxDir string
		asJSON     bool
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&asJSON, "json", false, "Emit the report as a JSON object instead of key=value lines")
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
		fmt.Fprintln(stderr, "pg_sandbox status: --sandbox-dir is required")
		usageHint(stderr, "status")
		return ui.ExitUsage.Int()
	}
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox status: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox status: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
	// StatusWithStderr emits warning lines for any failed
	// best-effort replication probe (see SPEC §6.4) so the user
	// sees why a sub-section was skipped without having to grep
	// server.log.
	rep, err := sandbox.StatusWithStderr(ctx, runner, sandboxDir, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox status: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}

	if asJSON {
		if err := rep.RenderJSON(stdout); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox status: marshal: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		return ui.ExitOK.Int()
	}
	rep.RenderText(stdout)
	return ui.ExitOK.Int()
}

// statusHelp prints `pg_sandbox help status`. SPEC §6.4.
func statusHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox status — report sandbox running/replication state")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox status -s <dir> [--json]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Prints whether the cluster is running, plus best-effort replication info")
	fmt.Fprintln(w, "(physical/logical slots, subscriptions). Probes that fail emit a warning")
	fmt.Fprintln(w, "on stderr but do not change the exit code.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
		{"    --json", "Emit the report as a JSON object instead of key=value lines"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.4.")
}
