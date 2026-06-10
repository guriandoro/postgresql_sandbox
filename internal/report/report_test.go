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
	if !strings.HasSuffix(opts.OutputPath, "report.html") {
		t.Errorf("default output should end with report.html, got %q", opts.OutputPath)
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
