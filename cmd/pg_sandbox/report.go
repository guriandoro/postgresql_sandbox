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
//
// We DO auto-discover scripts already on disk: when --pg-gather-dir /
// PGS_PG_GATHER_DIR / global pgGatherDir are all unset, we look in the
// current working directory and each $PATH entry for a directory
// holding both gather scripts and use the first match (logging it).
// That's discovery of existing files, not a download, so it doesn't
// contradict the SPEC's no-auto-download stance — it mirrors the
// latest-install bin-dir discovery above it.
//
// There IS a --destroy-on-failure / -D flag, but it does NOT suppress a
// prompt: it controls failure cleanup. On success the throwaway sandbox
// is always destroyed; on failure it is left on disk for debugging
// unless --destroy-on-failure is set, in which case it is torn down too.

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

	"github.com/guriandoro/postgresql_sandbox/internal/fsutil"
	"github.com/guriandoro/postgresql_sandbox/internal/report"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runReport is the dispatcher contract for `report`.
func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)

	var (
		inputPath        string
		outputPath       string
		binDir           string
		pgGatherDir      string
		sandboxRoot      string
		destroyOnFailure bool
	)
	fs.StringVar(&inputPath, "input", "", "Captured pg_gather out.txt (required)")
	fs.StringVar(&outputPath, "output", "", "Rendered HTML output path (default <input>_report.html next to --input)")
	fs.StringVar(&binDir, "bin-dir", "", "PostgreSQL bin/ directory (or set PGS_BIN_DIR; defaults to the latest install under /opt/postgresql)")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&pgGatherDir, "pg-gather-dir", "", "Directory with pg_gather scripts (or set PGS_PG_GATHER_DIR / pgGatherDir in global config)")
	fs.StringVar(&sandboxRoot, "root", "", "Sandbox root for the throwaway sandbox (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)")
	fs.BoolVar(&destroyOnFailure, "destroy-on-failure", false, "Destroy the throwaway sandbox even if report generation fails")
	fs.BoolVar(&destroyOnFailure, "D", false, "Alias for --destroy-on-failure")

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

	// --output is optional. When unset it's left empty here and the
	// report layer derives the default ("<input-base>_report.html"
	// alongside --input) in validateOptions. See SPEC §6.13.

	// Layered global config + env for bin-dir, pg-gather-dir,
	// sandbox-root. bin-dir is open-coded here (not via resolveBinDir)
	// because report's last resort differs: rather than blindly
	// defaulting to the /opt/postgresql parent (which holds no binaries
	// directly), it discovers the newest versioned install underneath.
	// pg-gather-dir follows the same layered shape with its own
	// ExitPgGatherDirMissing exit code.
	globalCfg := loadGlobalConfig()
	// bin-dir resolution: flag → PGS_BIN_DIR env → global.DefaultBinDir.
	if binDir == "" {
		binDir = os.Getenv("PGS_BIN_DIR")
	}
	if binDir == "" && globalCfg != nil {
		binDir = globalCfg.DefaultBinDir
	}
	// Nothing supplied a bin-dir. Rather than erroring, discover the
	// newest install under /opt/postgresql and use it — report takes no
	// <version> argument, so "use the latest one available" is the
	// sensible default. We only fall through to the usage error below
	// when no usable install exists there.
	if binDir == "" {
		if p, v, ok := latestInstalledBinDir(defaultInstallBase); ok {
			binDir = p
			fmt.Fprintf(stderr, "level=INFO msg=%q version=%q dir=%q\n",
				"report: no --bin-dir set; using latest install under "+defaultInstallBase, v, p)
		}
	}
	if binDir == "" {
		fmt.Fprintf(stderr,
			"pg_sandbox report: --bin-dir is required (or set PGS_BIN_DIR / global defaultBinDir); "+
				"no usable PostgreSQL install found under %s\n", defaultInstallBase)
		return ui.ExitUsage.Int()
	}
	binDir = fsutil.ExpandTilde(binDir)
	if !filepath.IsAbs(binDir) {
		if abs, err := filepath.Abs(binDir); err == nil {
			binDir = abs
		}
	}

	// pg-gather-dir resolution: flag → PGS_PG_GATHER_DIR env →
	// global.PgGatherDir → auto-discovery (CWD + $PATH). Empty
	// everywhere → ExitPgGatherDirMissing.
	if pgGatherDir == "" {
		pgGatherDir = os.Getenv("PGS_PG_GATHER_DIR")
	}
	if pgGatherDir == "" && globalCfg != nil {
		pgGatherDir = globalCfg.PgGatherDir
	}
	if pgGatherDir == "" {
		if found := discoverPgGatherDir(); found != "" {
			pgGatherDir = found
			fmt.Fprintf(stderr, "level=INFO msg=%q dir=%q\n",
				"report: no --pg-gather-dir set; using auto-discovered pg_gather scripts", found)
		}
	}
	if pgGatherDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox report: --pg-gather-dir is required (or set PGS_PG_GATHER_DIR or pgGatherDir in global config)")
		return ui.ExitPgGatherDirMissing.Int()
	}
	pgGatherDir = fsutil.ExpandTilde(pgGatherDir)
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
	inputPath = fsutil.ExpandTilde(inputPath)
	if !filepath.IsAbs(inputPath) {
		if abs, err := filepath.Abs(inputPath); err == nil {
			inputPath = abs
		}
	}
	// Only normalise --output when the user supplied one. An empty value
	// is passed through so report.validateOptions can derive the default
	// from --input — and filepath.Abs("") would otherwise resolve to the
	// CWD, turning the empty default into a directory path.
	if outputPath != "" {
		outputPath = fsutil.ExpandTilde(outputPath)
		if !filepath.IsAbs(outputPath) {
			if abs, err := filepath.Abs(outputPath); err == nil {
				outputPath = abs
			}
		}
	}

	selfPath, _ := os.Executable()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := report.Generate(ctx, report.Options{
		InputPath:        inputPath,
		OutputPath:       outputPath,
		BinDir:           binDir,
		PgGatherDir:      pgGatherDir,
		SandboxRoot:      sandboxRoot,
		SelfPath:         selfPath,
		DestroyOnFailure: destroyOnFailure,
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

// discoverPgGatherDir looks for a directory containing both pg_gather
// scripts when --pg-gather-dir / PGS_PG_GATHER_DIR / global pgGatherDir
// were all unset. It checks the current working directory first, then
// each entry in $PATH. Returns "" if no candidate qualifies. Env/CWD
// access lives here in the CLI layer; the internal/report package only
// exposes the filename-aware GatherDirHasScripts check.
func discoverPgGatherDir() string {
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	candidates = append(candidates, filepath.SplitList(os.Getenv("PATH"))...)
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if report.GatherDirHasScripts(dir) {
			return dir
		}
	}
	return ""
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
	fmt.Fprintln(w, "<input>_report.html next to --input). Prints the output path on stdout.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "When no bin-dir is given, the latest PostgreSQL install under /opt/postgresql")
	fmt.Fprintln(w, "is used automatically (existing binaries only — nothing is built).")
	fmt.Fprintln(w, "When no pg-gather-dir is given, the current directory and each $PATH entry are")
	fmt.Fprintln(w, "searched for one holding gather_schema.sql + gather_report.sql.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"    --input <path>", "Captured pg_gather out.txt (required)"},
		{"    --output <path>", "Rendered HTML output path (default <input>_report.html next to --input)"},
		{"-b, --bin-dir <dir>", "PostgreSQL bin/ directory (or set PGS_BIN_DIR / global defaultBinDir; defaults to the latest install under /opt/postgresql)"},
		{"    --pg-gather-dir <dir>", "Directory with pg_gather scripts (or set PGS_PG_GATHER_DIR / global pgGatherDir; auto-discovered from CWD and $PATH when unset)"},
		{"    --root <dir>", "Sandbox root for the throwaway sandbox (default $PGS_SANDBOX_ROOT or ~/postgresql-sandboxes/)"},
		{"-D, --destroy-on-failure", "Destroy the throwaway sandbox even if report generation fails (default: keep it for debugging)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.13.")
}
