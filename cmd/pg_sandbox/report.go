// CLI wiring for `pg_sandbox report`. SPEC §6.13.
//
// Flag resolution mirrors deploy/cluster: built-in default → global
// config → env → flag. For the gather-dir specifically, "missing
// everywhere" → ExitPgGatherDirMissing (not the generic ExitUsage),
// so users see a precise hint about which env var or config key to
// set.
//
// We do NOT implement an auto-download for --pg-gather-dir. SPEC
// §6.13 says "refuses by default rather than auto-downloading" — the
// missing-everywhere case is a hard error with a precise hint, not a
// prompt, so there is no prompt to suppress and no --force flag.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/internal/report"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runReport is the dispatcher contract for `report`.
func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)

	var (
		inputPath   string
		outputPath  string
		binDir      string
		pgGatherDir string
		sandboxRoot string
	)
	fs.StringVar(&inputPath, "input", "", "Captured pg_gather out.txt (required)")
	fs.StringVar(&outputPath, "output", "", "Rendered HTML output path (default report.html in CWD)")
	fs.StringVar(&binDir, "bin-dir", "", "PostgreSQL bin/ directory (or set PGS_BIN_DIR)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&pgGatherDir, "pg-gather-dir", "", "Directory with pg_gather scripts (or set PGS_PG_GATHER_DIR / pgGatherDir in global config)")
	fs.StringVar(&sandboxRoot, "root", "", "Sandbox root for the throwaway sandbox (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)")

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)

	// Resolve --input. Required, no env fallback.
	if inputPath == "" {
		fmt.Fprintln(stderr, "pg_sandbox report: --input is required")
		usageHint(stderr, "report")
		return ui.ExitUsage.Int()
	}

	// Resolve --output. Default report.html in CWD per SPEC §6.13.
	if outputPath == "" {
		outputPath = "report.html"
	}

	// Layered global config + env for bin-dir, pg-gather-dir,
	// sandbox-root. bin-dir is open-coded here (not via
	// resolveBinDir) because report has no built-in default for it —
	// missing-everywhere is an error, not a fallback to
	// /opt/postgresql. pg-gather-dir follows the same shape with its
	// own ExitPgGatherDirMissing exit code.
	globalCfg := loadGlobalConfig()
	// bin-dir resolution: flag → PGS_BIN_DIR env → global.DefaultBinDir.
	if binDir == "" {
		binDir = os.Getenv("PGS_BIN_DIR")
	}
	if binDir == "" && globalCfg != nil {
		binDir = globalCfg.DefaultBinDir
	}
	if binDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox report: --bin-dir is required (or set PGS_BIN_DIR / global defaultBinDir)")
		return ui.ExitUsage.Int()
	}
	if !filepath.IsAbs(binDir) {
		if abs, err := filepath.Abs(binDir); err == nil {
			binDir = abs
		}
	}

	// pg-gather-dir resolution: flag → PGS_PG_GATHER_DIR env →
	// global.PgGatherDir. Empty everywhere → ExitPgGatherDirMissing.
	if pgGatherDir == "" {
		pgGatherDir = os.Getenv("PGS_PG_GATHER_DIR")
	}
	if pgGatherDir == "" && globalCfg != nil {
		pgGatherDir = globalCfg.PgGatherDir
	}
	if pgGatherDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox report: --pg-gather-dir is required (or set PGS_PG_GATHER_DIR or pgGatherDir in global config)")
		return ui.ExitPgGatherDirMissing.Int()
	}
	if !filepath.IsAbs(pgGatherDir) {
		if abs, err := filepath.Abs(pgGatherDir); err == nil {
			pgGatherDir = abs
		}
	}

	// sandbox-root resolution: flag → PGS_SANDBOX_ROOT env →
	// global.SandboxRoot → ~/postgresql-sandboxes/. Same chain as
	// global_status; consolidated in resolveSandboxRoot. We pass
	// globalCfg (already loaded above) so we don't reread the file.
	var err error
	sandboxRoot, err = resolveSandboxRoot(sandboxRoot, globalCfg)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox report: %v\n", err)
		return ui.ExitGeneric.Int()
	}

	// Normalise --input / --output to absolute paths so error messages
	// and the report writer agree on what they point at.
	if !filepath.IsAbs(inputPath) {
		if abs, err := filepath.Abs(inputPath); err == nil {
			inputPath = abs
		}
	}
	if !filepath.IsAbs(outputPath) {
		if abs, err := filepath.Abs(outputPath); err == nil {
			outputPath = abs
		}
	}

	selfPath, _ := os.Executable()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := report.Generate(ctx, report.Options{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		BinDir:      binDir,
		PgGatherDir: pgGatherDir,
		SandboxRoot: sandboxRoot,
		SelfPath:    selfPath,
	}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox report: %v\n", err)
		// If a throwaway sandbox was left behind, name it so the user
		// can `destroy --force` it manually after debugging.
		var le *report.LeftoverError
		if errors.As(err, &le) {
			fmt.Fprintf(stderr,
				"pg_sandbox report: throwaway sandbox at %s — destroy with `pg_sandbox destroy -s %s --force`\n",
				le.Dir, le.Dir)
		}
		return report.ExitCodeFor(err).Int()
	}
	// SPEC §4.6: stdout for machine-consumable output. The output
	// path is what users will pipe to a browser or copy elsewhere,
	// so it's the right thing to put there.
	fmt.Fprintln(stdout, res.OutputPath)
	if res.ThrowawaySandboxDir != "" {
		fmt.Fprintf(stderr, "level=WARN msg=%q dir=%q\n",
			"report: throwaway sandbox could not be destroyed; clean up by hand",
			res.ThrowawaySandboxDir)
	}
	return ui.ExitOK.Int()
}

// reportHelp prints `pg_sandbox help report`. SPEC §6.13.
func reportHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox report — generate a pg_gather HTML report")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox report --input <out.txt> --pg-gather-dir <dir> --bin-dir <dir> [--output <html>]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Loads a captured pg_gather out.txt into a throwaway sandbox, runs the gather")
	fmt.Fprintln(w, "report scripts against it, and writes the rendered HTML to --output (default")
	fmt.Fprintln(w, "report.html in CWD). Prints the output path on stdout.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"    --input <path>", "Captured pg_gather out.txt (required)"},
		{"    --output <path>", "Rendered HTML output path (default report.html in CWD)"},
		{"-b, --bin-dir <dir>", "PostgreSQL bin/ directory (or set PGS_BIN_DIR / global defaultBinDir)"},
		{"    --pg-gather-dir <dir>", "Directory with pg_gather scripts (or set PGS_PG_GATHER_DIR / global pgGatherDir)"},
		{"    --root <dir>", "Sandbox root for the throwaway sandbox (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.13.")
}
