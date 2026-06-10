// pg_gather report generation pipeline. SPEC §6.13.
//
// Generate composes the existing sandbox deploy / psql / destroy
// primitives into a single end-to-end command:
//
//   1. Validate inputs (--input file exists, --pg-gather-dir set and
//      contains the gather scripts).
//   2. Deploy a throwaway sandbox UNDER the configured sandbox root
//      so it's visible to `global_status` while the report is being
//      generated. We use a tempdir name like "_report_<random>" so
//      the dir is recognizable as ephemeral.
//   3. Pipe gather_schema.sql followed by the raw out.txt into
//      `psql -X -v ON_ERROR_STOP=1`. This combines the schema load
//      and data ingestion in one psql session; the upstream
//      generate_report.sh shell wrapper uses the same trick (see
//      https://github.com/percona/support-snippets/blob/main/postgresql/pg_gather/generate_report.sh).
//   4. Pipe gather_report.sql into a second psql call and capture
//      stdout into --output. The gather_report.sql file emits HTML
//      via \echo directives, so we don't need to enable any
//      formatting flags.
//   5. On success: destroy the throwaway sandbox (--force), print
//      the output file path, return 0.
//   6. On failure (anywhere after the deploy): leave the throwaway
//      sandbox on disk, print its path for debugging, return
//      ExitReportFailed. With Options.DestroyOnFailure (CLI
//      --destroy-on-failure / -D), the sandbox is destroyed here too
//      instead of being left behind.
//
// Expected pg_gather script filenames (assumed; see SPEC §6.13):
//
//   - gather_schema.sql — `CREATE TABLE pg_gather_…` plus support
//     functions. Loaded BEFORE the data.
//   - gather_report.sql — `\echo` directives that emit HTML.
//
// These are the canonical names in Percona's pg_gather repository
// (the version this tool integrates with) — confirmed against
// ~/src/support-snippets/postgresql/pg_gather/ on the dev machine.
// If a user points --pg-gather-dir at a directory missing either
// file, we fail-fast with a clear error BEFORE deploying anything.

package report

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// Re-exported exit codes for the package's failure modes. Mirrors
// internal/sandbox/errors.go's pattern.
const (
	ExitOK                 = ui.ExitOK
	ExitUsage              = ui.ExitUsage
	ExitPgGatherDirMissing = ui.ExitPgGatherDirMissing
	ExitReportFailed       = ui.ExitReportFailed
)

// Canonical names of the two gather scripts we shell into psql.
// Constants here so a single grep finds every reference if the
// upstream ever renames them.
const (
	gatherSchemaSQL = "gather_schema.sql"
	gatherReportSQL = "gather_report.sql"
)

// Options captures the inputs to Generate. The CLI layer populates
// this from flag/env/global-config resolution and hands it off; this
// package never touches flag.FlagSet or os.Getenv itself.
type Options struct {
	// InputPath is the captured pg_gather out.txt to ingest.
	// REQUIRED; Generate refuses an empty value with ExitUsage.
	InputPath string

	// OutputPath is where the rendered HTML report is written.
	// Defaults to "report.html" in the CWD; the CLI layer fills in
	// the default before calling Generate so this field is always
	// populated when we get it.
	OutputPath string

	// BinDir is the PostgreSQL bin/ directory for the throwaway
	// sandbox. REQUIRED — Generate cannot deploy without it.
	BinDir string

	// PgGatherDir is the directory holding gather_schema.sql and
	// gather_report.sql. REQUIRED — Generate refuses with
	// ExitPgGatherDirMissing if empty or if the scripts aren't
	// present.
	PgGatherDir string

	// SandboxRoot is where the throwaway sandbox will be created.
	// REQUIRED — Generate refuses to put it in /tmp because then
	// global_status wouldn't see it during its (short) lifetime.
	SandboxRoot string

	// SelfPath, when non-empty, is the absolute path of the
	// pg_sandbox binary baked into the throwaway sandbox's
	// convenience scripts. The throwaway sandbox is destroyed at the
	// end of Generate, so this rarely matters; we forward it for
	// consistency with deploy.
	SelfPath string

	// DestroyOnFailure, when true, destroys the throwaway sandbox even
	// when the pipeline fails, instead of leaving it on disk for
	// debugging (the default). Maps to the CLI --destroy-on-failure / -D
	// flag. The success path always destroys the sandbox regardless.
	DestroyOnFailure bool

	// Runner, when non-nil, is used instead of pgexec.New(BinDir).
	// Tests use this to inject a pgexec.Fake. Production callers
	// leave it nil and Generate constructs a real *pgexec.Exec from
	// BinDir.
	Runner pgexec.Runner
}

// Result is what Generate returns on success.
type Result struct {
	// OutputPath is the absolute path of the rendered HTML report.
	OutputPath string

	// ThrowawaySandboxDir is the dir of the throwaway sandbox that
	// was deployed. Always empty on success (sandbox was destroyed);
	// on failure, callers can find this in the returned error wrapped
	// via leftoverError so they can tell the user where to look.
	ThrowawaySandboxDir string
}

// LeftoverError is returned (via errors.As) when Generate failed after
// the throwaway sandbox was deployed. The caller can pull the dir out
// to print "sandbox left at X for debugging".
type LeftoverError struct {
	Dir string
	Err error
}

func (e *LeftoverError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("report: pipeline failed; throwaway sandbox left at %s", e.Dir)
	}
	return fmt.Sprintf("%v (throwaway sandbox left at %s)", e.Err, e.Dir)
}

func (e *LeftoverError) Unwrap() error { return e.Err }

// Generate runs the SPEC §6.13 pipeline. See package docs for the
// step-by-step.
//
// Diagnostic output (info-level lines describing the current step,
// warnings) is written to stderrW. The output HTML is written to
// the path in opts.OutputPath; nothing report-related is written to
// stdout — the CLI layer is responsible for printing "report written
// to X" if it wants to.
func Generate(ctx context.Context, opts Options, stderrW io.Writer) (*Result, error) {
	if err := validateOptions(&opts); err != nil {
		return nil, err
	}

	// Step 1: verify the input file exists. We check this FIRST so a
	// typo'd --input path surfaces as a plain usage error rather than
	// the rarer ExitPgGatherDirMissing — that ordering matches users'
	// expectations ("the file I named doesn't exist").
	if _, err := os.Stat(opts.InputPath); err != nil {
		return nil, &exitErr{
			Code: ExitUsage,
			Err:  fmt.Errorf("--input file not readable: %s: %w", opts.InputPath, err),
		}
	}
	// Step 1b: verify the gather scripts exist BEFORE we deploy
	// anything. Failing here is cheap and saves the user a pointless
	// deploy+destroy round-trip if their gather dir is wrong.
	schemaPath := filepath.Join(opts.PgGatherDir, gatherSchemaSQL)
	reportPath := filepath.Join(opts.PgGatherDir, gatherReportSQL)
	for _, p := range []string{schemaPath, reportPath} {
		if _, err := os.Stat(p); err != nil {
			return nil, &exitErr{
				Code: ExitPgGatherDirMissing,
				Err: fmt.Errorf("pg_gather script not found at %s: %w (set --pg-gather-dir or PGS_PG_GATHER_DIR)",
					p, err),
			}
		}
	}

	// Step 2: deploy a throwaway sandbox under the sandbox root. We
	// name it "_report_<random>" so global_status (and humans
	// scanning the root) immediately see it as ephemeral.
	tag, err := randomTag()
	if err != nil {
		return nil, fmt.Errorf("report: random tag: %w", err)
	}
	throwawayDir := filepath.Join(opts.SandboxRoot, "_report_"+tag)
	if err := os.MkdirAll(opts.SandboxRoot, 0o755); err != nil {
		return nil, fmt.Errorf("report: mkdir sandbox root %s: %w", opts.SandboxRoot, err)
	}

	fmt.Fprintf(stderrW, "level=INFO msg=%q dir=%q\n",
		"report: deploying throwaway sandbox", throwawayDir)

	// The runner is constructed with the user-supplied bin-dir. It is
	// reused for both deploy and the psql piping (psql lives next to
	// initdb / pg_ctl in the same install). Reuse also keeps debug
	// output consistent across all sub-process invocations.
	//
	// Tests inject opts.Runner; production passes nil and we build a
	// real *pgexec.Exec here.
	var runner pgexec.Runner
	if opts.Runner != nil {
		runner = opts.Runner
	} else {
		runner = pgexec.New(opts.BinDir)
	}

	deployRes, err := sandbox.Deploy(ctx, runner, sandbox.DeployOptions{
		SandboxDir: throwawayDir,
		BinDir:     opts.BinDir,
		SelfPath:   opts.SelfPath,
	}, stderrW)
	if err != nil {
		// Deploy failed BEFORE the sandbox dir was meaningful;
		// nothing to leave behind for debugging. Return the deploy
		// error directly so the CLI can map its exit code.
		return nil, fmt.Errorf("report: deploy throwaway sandbox: %w", err)
	}
	cfg := deployRes.Sandbox

	// From here on, any failure leaves the sandbox in place for
	// debugging — wrap in LeftoverError. With --destroy-on-failure, we
	// instead tear the sandbox down here and return a plain error (no
	// LeftoverError, since nothing is left behind).
	leftover := func(inner error) error {
		if opts.DestroyOnFailure {
			fmt.Fprintf(stderrW, "level=INFO msg=%q dir=%q\n",
				"report: --destroy-on-failure set; destroying throwaway sandbox after failure", throwawayDir)
			// Detached context: the failure may itself be a ctx
			// cancellation (SIGINT), but we still want the cleanup to
			// run to completion.
			cleanupCtx := context.WithoutCancel(ctx)
			if derr := sandbox.Destroy(cleanupCtx, runner,
				sandbox.DestroyOptions{SandboxDir: throwawayDir}, stderrW); derr != nil {
				// Cleanup itself failed: fall back to leaving the
				// sandbox behind so the user is told where it is.
				fmt.Fprintf(stderrW, "level=WARN msg=%q dir=%q err=%q\n",
					"report: --destroy-on-failure cleanup failed; sandbox left behind",
					throwawayDir, derr.Error())
				return &exitErr{Code: ExitReportFailed, Err: &LeftoverError{Dir: throwawayDir, Err: inner}}
			}
			return &exitErr{Code: ExitReportFailed, Err: inner}
		}
		return &exitErr{
			Code: ExitReportFailed,
			Err:  &LeftoverError{Dir: throwawayDir, Err: inner},
		}
	}

	// Step 3: pipe gather_schema.sql + the input out.txt into one
	// psql session. The upstream generate_report.sh uses
	// `{ cat schema; cat out; }  | psql -f -` so the schema is
	// applied first and the COPY data (the bulk of out.txt) lands
	// against the just-created tables.
	fmt.Fprintf(stderrW, "level=INFO msg=%q\n", "report: loading schema + ingesting input")
	stdin, cleanup, err := concatReader(schemaPath, opts.InputPath)
	if err != nil {
		return nil, leftover(err)
	}
	defer cleanup()

	// We use -v ON_ERROR_STOP=1 so a malformed schema or COPY
	// failure surfaces as a non-zero psql exit, NOT as a half-loaded
	// database the report runs against silently. The schema script
	// itself does its own ANALYZE; we don't add one.
	res := runner.RunWithStdin(ctx, stdin, "psql",
		"-X", "-v", "ON_ERROR_STOP=1",
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-f", "-",
	)
	if res.Err != nil || res.ExitCode != 0 {
		writeStderr(stderrW, "psql load schema", res.Stderr)
		return nil, leftover(fmt.Errorf("psql (schema+ingest) exit=%d: %w", res.ExitCode, res.Err))
	}

	// Step 4: run gather_report.sql with stdout captured to a file.
	// Upstream uses `psql -X -f - > report.html`; we do the same
	// pattern with a file in Stdout via runCaptured (the runner
	// captures stdout into a Result, then we persist it to disk).
	fmt.Fprintf(stderrW, "level=INFO msg=%q output=%q\n",
		"report: rendering HTML report", opts.OutputPath)
	reportFile, err := os.Open(reportPath)
	if err != nil {
		return nil, leftover(fmt.Errorf("open %s: %w", reportPath, err))
	}
	defer reportFile.Close()

	res = runner.RunWithStdin(ctx, reportFile, "psql",
		"-X",
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-f", "-",
	)
	// We intentionally do NOT pass -v ON_ERROR_STOP=1 here. The
	// gather_report.sql script issues many \echo + SELECT pairs and a
	// handful of queries are version-conditional (they reference
	// catalog columns that may not exist on every supported PG
	// version). Tolerating per-query errors matches what upstream's
	// generate_report.sh does: it just redirects stdout and trusts
	// the user to scroll past any error lines in the HTML.
	if res.Err != nil {
		// A process-level error (couldn't start psql, signal) is
		// fatal; a non-zero exit alone is not.
		writeStderr(stderrW, "psql render report", res.Stderr)
		return nil, leftover(fmt.Errorf("psql (render report) failed: %w", res.Err))
	}
	if err := os.WriteFile(opts.OutputPath, res.Stdout, 0o644); err != nil {
		return nil, leftover(fmt.Errorf("write report %s: %w", opts.OutputPath, err))
	}

	// Step 5: destroy the throwaway sandbox on success. We pass
	// the same runner; sandbox.Destroy handles "is it running" itself.
	fmt.Fprintf(stderrW, "level=INFO msg=%q dir=%q\n",
		"report: destroying throwaway sandbox", throwawayDir)
	if err := sandbox.Destroy(ctx, runner, sandbox.DestroyOptions{SandboxDir: throwawayDir}, stderrW); err != nil {
		// Destroy failure here is unexpected (we just deployed it
		// successfully). We DO NOT mask the report success: the
		// report file exists and is what the user asked for. Surface
		// the destroy failure as a warning and return success — but
		// include the throwaway dir in the result so the user can
		// clean up by hand if they want.
		fmt.Fprintf(stderrW, "level=WARN msg=%q dir=%q err=%q\n",
			"report: destroy throwaway sandbox failed; clean up by hand",
			throwawayDir, err.Error())
		return &Result{
			OutputPath:          opts.OutputPath,
			ThrowawaySandboxDir: throwawayDir,
		}, nil
	}

	return &Result{OutputPath: opts.OutputPath}, nil
}

// validateOptions normalises and rejects misuse. Mirrors the pattern
// in sandbox.normalizeDeployOptions: caller-visible errors are
// pre-tagged with ExitUsage so the CLI maps them correctly.
func validateOptions(opts *Options) error {
	if opts.InputPath == "" {
		return &exitErr{Code: ExitUsage, Err: errors.New("report.Generate: --input is required")}
	}
	if opts.OutputPath == "" {
		// Default per SPEC §6.13: report.html in CWD. The CLI layer
		// usually fills this in; we re-apply the default here so
		// programmatic callers get the same behavior.
		opts.OutputPath = "report.html"
	}
	if !filepath.IsAbs(opts.OutputPath) {
		abs, err := filepath.Abs(opts.OutputPath)
		if err != nil {
			return fmt.Errorf("report.Generate: abs(%s): %w", opts.OutputPath, err)
		}
		opts.OutputPath = abs
	}
	if opts.BinDir == "" {
		return &exitErr{Code: ExitUsage, Err: errors.New("report.Generate: --bin-dir is required")}
	}
	if opts.PgGatherDir == "" {
		return &exitErr{
			Code: ExitPgGatherDirMissing,
			Err:  errors.New("report.Generate: --pg-gather-dir is required (also accepts PGS_PG_GATHER_DIR or pgGatherDir in global config)"),
		}
	}
	if opts.SandboxRoot == "" {
		return &exitErr{Code: ExitUsage, Err: errors.New("report.Generate: SandboxRoot is required")}
	}
	return nil
}

// concatReader returns an io.Reader that emits the contents of
// paths[0], paths[1], … in order. We use io.MultiReader plus deferred
// closes so the caller can pipe a single stream into psql's stdin
// without buffering all of it into memory (out.txt can be large).
//
// The returned cleanup MUST be called to close any opened files.
func concatReader(paths ...string) (io.Reader, func(), error) {
	opened := make([]*os.File, 0, len(paths))
	cleanup := func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}
	readers := make([]io.Reader, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("open %s: %w", p, err)
		}
		opened = append(opened, f)
		readers = append(readers, f)
	}
	return io.MultiReader(readers...), cleanup, nil
}

// randomTag returns 8 hex characters of cryptographically-random
// data. Used to name the throwaway sandbox dir uniquely so two
// concurrent `report` invocations don't clash.
func randomTag() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// writeStderr writes a single structured-log line summarising the
// stderr captured from a failed child process. Mirrors
// sandbox.emitStderr but kept local so this package doesn't reach
// into another package's private helpers.
func writeStderr(w io.Writer, what string, b []byte) {
	if len(b) == 0 {
		return
	}
	// Drop trailing newline so the structured-log line stays single.
	trimmed := string(b)
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == '\r') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if trimmed == "" {
		return
	}
	fmt.Fprintf(w, "level=ERROR msg=%q output=%q\n", what+" stderr", trimmed)
}
