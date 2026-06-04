// CLI wiring for `pg_sandbox cleanup-install-versions`. SPEC §7.2.
//
// Owns flag parsing, resolution of PGS_BIN_DIR + PGS_SANDBOX_ROOT, TTY
// detection, and the y/N prompt. The actual inventory + cross-reference
// + RemoveAll logic is in internal/cleanup.
//
// Exit-code policy:
//   - 0 on success (including the "nothing to remove" case).
//   - 2 (ExitUsage) on unknown version name in the positional args.
//   - 27 (ExitNotATTY) when stdin isn't a TTY and --force is unset.
//   - 1 (ExitGeneric) on RemoveAll failure (rare, e.g. permission).

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/guriandoro/postgresql_sandbox/go/internal/cleanup"
	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runCleanupInstallVersions is the dispatcher contract.
func runCleanupInstallVersions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cleanup-install-versions", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		force       bool
		binDir      string
		sandboxRoot string
	)
	fs.BoolVar(&force, "force", false, "Skip confirmation prompt")
	fs.BoolVar(&force, "f", false, "Alias for --force")
	fs.StringVar(&binDir, "bin-dir", "", "Install root to prune (default $PGS_BIN_DIR or global defaultBinDir)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&sandboxRoot, "root", "", "Sandbox root to walk (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)")

	// Pre-process argv so users can put the bool --force / -f flag
	// AFTER positional version names. Go's stdlib `flag` stops at the
	// first non-flag, so without this step `cleanup-install-versions
	// 18.3 --force` treats `--force` as a positional version and the
	// prompt fires (defeating the user's intent). Only bool flags are
	// reordered; --bin-dir / --root take values and must stay
	// adjacent to them. See argv.go for the full contract.
	//
	// Bool flag names are derived from the FlagSet so a new BoolVar
	// above doesn't silently re-introduce the original UX bug.
	args = reorderBoolFlags(args, boolFlagNames(fs))

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	onlyVersions := fs.Args()

	// Layered resolution: flag → env → global config → default.
	var globalCfg *config.Global
	if gp, err := config.GlobalConfigPath(); err == nil {
		if g, gerr := config.LoadGlobal(gp); gerr == nil {
			globalCfg = g
		}
	}
	if binDir == "" {
		binDir = os.Getenv("PGS_BIN_DIR")
	}
	if binDir == "" && globalCfg != nil {
		binDir = globalCfg.DefaultBinDir
	}
	if binDir == "" {
		// Same default as build's bin-dir resolution.
		binDir = "/opt/postgresql"
	}
	if !filepath.IsAbs(binDir) {
		if abs, err := filepath.Abs(binDir); err == nil {
			binDir = abs
		}
	}

	if sandboxRoot == "" {
		sandboxRoot = os.Getenv("PGS_SANDBOX_ROOT")
	}
	if sandboxRoot == "" && globalCfg != nil {
		sandboxRoot = globalCfg.SandboxRoot
	}
	if sandboxRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: cannot determine home dir: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		sandboxRoot = filepath.Join(home, "postgresql-sandboxes")
	}
	// Normalize relative sandboxRoot (env / global config values) the
	// same way we do for binDir above. The default home-joined path is
	// already absolute, but a user with PGS_SANDBOX_ROOT=./sandboxes in
	// their shell rc would otherwise get a banner that prints the
	// relative string and a collectSandboxBinDirs walk against whatever
	// CWD pg_sandbox happened to be invoked from — defeating the
	// 2026-06-04 defense-in-depth banner (see RenderPlan's doc and
	// cleanup-install-versions-pitfall.md). Abs'ing here keeps the
	// banner honest and the cross-reference correct.
	if !filepath.IsAbs(sandboxRoot) {
		if abs, err := filepath.Abs(sandboxRoot); err == nil {
			sandboxRoot = abs
		}
	}

	plan, err := cleanup.Plan(cleanup.Options{
		BinDir:       binDir,
		SandboxRoot:  sandboxRoot,
		OnlyVersions: onlyVersions,
		Force:        force,
	}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: %v\n", err)
		return cleanup.ExitCodeFor(err).Int()
	}

	// Always print the plan to stdout so users (and tests) can see
	// what's tracked even when nothing is removable. The resolved
	// bin dir and sandbox root are both passed so RenderPlan can
	// announce the full scope in its header (defense-in-depth for
	// the 2026-06-04 incident; see internal/cleanup/cleanup.go's
	// RenderPlan doc).
	cleanup.RenderPlan(stdout, binDir, sandboxRoot, plan)

	// Count unused candidates. Nothing to do → 0 exit, message.
	unused := 0
	for _, c := range plan {
		if c.IsUnused() {
			unused++
		}
	}
	if unused == 0 {
		fmt.Fprintln(stderr, "no unused install versions to remove")
		return ui.ExitOK.Int()
	}

	if !force {
		if !stdinIsTTY() {
			fmt.Fprintln(stderr, "pg_sandbox cleanup-install-versions: stdin is not a TTY and --force was not set; refusing")
			return ui.ExitNotATTY.Int()
		}
		if !cleanup.Confirm(os.Stdin, stderr, unused) {
			fmt.Fprintln(stderr, "pg_sandbox cleanup-install-versions: aborted")
			return ui.ExitOK.Int()
		}
	}

	removed, err := cleanup.Apply(plan, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: %v\n", err)
		// We still report how many were removed for triage.
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: removed %d before failure\n", removed)
		return ui.ExitGeneric.Int()
	}
	fmt.Fprintf(stderr, "level=INFO msg=%q removed=%d\n", "cleanup complete", removed)
	return ui.ExitOK.Int()
}
