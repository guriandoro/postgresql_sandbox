// CLI wiring for `pg_sandbox global_status`. SPEC §6.12.
//
// Flags: --root <path> (default PGS_SANDBOX_ROOT env, then ~/postgresql-sandboxes/),
// --json. No others — SPEC §6.12 is deliberately small.
//
// The walk itself lives in internal/sandbox/global_status.go; this
// file is purely the dispatcher: parse flags, resolve root, call
// sandbox.GlobalStatusWalk, render to stdout.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runGlobalStatus is the dispatcher contract for `global_status`.
func runGlobalStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("global_status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		root   string
		asJSON bool
	)
	fs.StringVar(&root, "root", "", "Sandbox root to walk (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)")
	fs.BoolVar(&asJSON, "json", false, "Emit machine-readable JSON to stdout")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}

	// SPEC §3.1 layered resolution: flag → env → global config →
	// built-in default. We consult global config first because it's
	// cheap; if it doesn't exist we silently fall back.
	if root == "" {
		root = os.Getenv("PGS_SANDBOX_ROOT")
	}
	if root == "" {
		// Try the global config (SPEC §3.3 / §6.12).
		if gp, perr := config.GlobalConfigPath(); perr == nil {
			if g, gerr := config.LoadGlobal(gp); gerr == nil && g != nil && g.SandboxRoot != "" {
				root = g.SandboxRoot
			}
		}
	}
	if root == "" {
		// Final fallback per SPEC §4.9: ~/postgresql-sandboxes/.
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox global_status: cannot determine home dir: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		root = filepath.Join(home, "postgresql-sandboxes")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	gs, err := sandbox.GlobalStatusWalk(ctx, sandbox.GlobalStatusOptions{Root: root}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox global_status: %v\n", err)
		return ui.ExitGeneric.Int()
	}
	if asJSON {
		if err := gs.RenderJSON(stdout); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox global_status: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		return ui.ExitOK.Int()
	}
	gs.RenderText(stdout)
	return ui.ExitOK.Int()
}
