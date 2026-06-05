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

	"github.com/guriandoro/postgresql_sandbox/go/internal/cleanup"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runCleanupInstallVersions is the dispatcher contract.
func runCleanupInstallVersions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cleanup-install-versions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)

	var (
		force       bool
		binDir      string
		sandboxRoot string
	)
	fs.BoolVar(&force, "force", false, "Skip confirmation prompt")
	fs.BoolVar(&force, "f", false, "Alias for --force")
	fs.StringVar(&binDir, "bin-dir", "", "Install root to prune (default $PGS_BIN_DIR, global defaultBinDir, or /opt/postgresql)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&sandboxRoot, "root", "", "Sandbox root to walk (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)")

	// Reorder bool flags ahead of positionals so `cleanup-install-
	// versions 18.4 --force` works. See parseSubcommandArgs in argv.go
	// for the full rationale and the structural reason this is a
	// single call rather than a two-step pattern.
	if err := parseSubcommandArgs(fs, args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	onlyVersions := fs.Args()

	// Layered resolution: flag → env → global config → default. Both
	// helpers filepath.Abs the result, which Cleans internally — so
	// trailing slashes and redundant separators (e.g.
	// `--bin-dir /opt/postgresql/`, `PGS_SANDBOX_ROOT=./sandboxes`)
	// are normalized before they reach RenderPlan and
	// cleanup.Plan. Without this the banner would print a path
	// textually different from the one actually walked — defeating
	// the 2026-06-04 defense-in-depth banner (see RenderPlan's doc
	// and cleanup-install-versions-pitfall.md).
	globalCfg := loadGlobalConfig()
	var err error
	binDir, err = resolveBinDir(binDir, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: %v\n", err)
		return ui.ExitGeneric.Int()
	}
	sandboxRoot, err = resolveSandboxRoot(sandboxRoot, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: %v\n", err)
		return ui.ExitGeneric.Int()
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
	for _, c := range plan.Candidates {
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

	removed, err := cleanup.Apply(plan.Candidates, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: %v\n", err)
		// We still report how many were removed for triage.
		fmt.Fprintf(stderr, "pg_sandbox cleanup-install-versions: removed %d before failure\n", removed)
		return ui.ExitGeneric.Int()
	}
	fmt.Fprintf(stderr, "level=INFO msg=%q removed=%d\n", "cleanup complete", removed)
	return ui.ExitOK.Int()
}

// cleanupInstallVersionsHelp prints help for `cleanup-install-versions`.
// SPEC §7.2.
func cleanupInstallVersionsHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox cleanup-install-versions — prune unused PostgreSQL install dirs")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox cleanup-install-versions [--bin-dir <dir>] [--root <dir>] [--force] [<version>...]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Walks <bin-dir> for installed PG versions, cross-references which ones are")
	fmt.Fprintln(w, "used by sandboxes under <root>, and removes the unused ones after a y/N prompt.")
	fmt.Fprintln(w, "Positional <version>s narrow the scan to just those names.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Caveat: only sandboxes under the resolved --root are scanned to mark versions")
	fmt.Fprintln(w, "as in-use. Sandboxes that live elsewhere will NOT block deletion — pass --root")
	fmt.Fprintln(w, "explicitly when sandboxes span multiple roots.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-b, --bin-dir <dir>", "Install root to prune (default $PGS_BIN_DIR, global defaultBinDir, or /opt/postgresql)"},
		{"    --root <dir>", "Sandbox root to walk (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)"},
		{"-f, --force", "Skip confirmation prompt"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §7.2.")
}
