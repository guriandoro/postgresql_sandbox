// CLI wiring for `pg_sandbox report`. SPEC §6.13.
//
// Flag resolution mirrors deploy/cluster: built-in default → global
// config → env → flag. For the gather-dir specifically, "missing
// everywhere" → ExitPgGatherDirMissing (not the generic ExitUsage),
// so users see a precise hint about which env var or config key to
// set.
//
// We do NOT implement an auto-download for --pg-gather-dir. SPEC
// §6.13 explicitly says "refuses by default rather than auto-
// downloading"; the --force flag is reserved for prompt-suppression
// when we add a future "you have the gather scripts; should we
// reuse them?" question, but in this slice --force is accepted and
// ignored. We document that in the help text.

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

	"github.com/guriandoro/postgresql_sandbox/go/internal/report"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runReport is the dispatcher contract for `report`.
func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		inputPath   string
		outputPath  string
		binDir      string
		pgGatherDir string
		sandboxRoot string
		forceUnused bool // accepted but unused this slice; documented below.
	)
	fs.StringVar(&inputPath, "input", "", "Captured pg_gather out.txt (required)")
	fs.StringVar(&outputPath, "output", "", "Rendered HTML output path (default report.html in CWD)")
	fs.StringVar(&binDir, "bin-dir", "", "PostgreSQL bin/ directory (or set PGS_BIN_DIR)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&pgGatherDir, "pg-gather-dir", "", "Directory with pg_gather scripts (or set PGS_PG_GATHER_DIR / pgGatherDir in global config)")
	fs.StringVar(&sandboxRoot, "root", "", "Sandbox root for the throwaway sandbox (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)")
	// SPEC §6.13 mentions --force/-f as a prompt-suppression knob for
	// the "missing gather dir" case. We don't implement the prompt in
	// this slice (we error fast instead), so --force is accepted to
	// keep the documented surface stable but has no effect today.
	fs.BoolVar(&forceUnused, "force", false, "Accepted but currently unused (reserved for prompt suppression)")
	fs.BoolVar(&forceUnused, "f", false, "Alias for --force")

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}

	// Resolve --input. Required, no env fallback.
	if inputPath == "" {
		fmt.Fprintln(stderr, "pg_sandbox report: --input is required")
		fs.Usage()
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
