// Tests for the real Exec implementation.
//
// We use coreutils-equivalent commands available on both macOS and
// Linux developer machines: /bin/sh, /bin/echo, /usr/bin/env. None
// of these are PostgreSQL binaries — that's fine. We are testing
// the *runner*, not its argv construction for any particular tool.

package pgexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesStdoutAndStderr(t *testing.T) {
	// `sh -c` lets us write to both streams in one process.
	e := &Exec{}
	res := e.Run(context.Background(), "/bin/sh",
		"-c", "printf hello; printf world >&2; exit 0")
	if res.Err != nil {
		t.Fatalf("Run error: %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if string(res.Stdout) != "hello" {
		t.Errorf("Stdout: got %q, want %q", res.Stdout, "hello")
	}
	if string(res.Stderr) != "world" {
		t.Errorf("Stderr: got %q, want %q", res.Stderr, "world")
	}
}

func TestRunNonZeroExit(t *testing.T) {
	e := &Exec{}
	res := e.Run(context.Background(), "/bin/sh", "-c", "exit 7")
	if res.Err != nil {
		t.Errorf("Run returned err for clean non-zero exit: %v", res.Err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode: got %d, want 7", res.ExitCode)
	}
}

func TestRunWithStdinPipes(t *testing.T) {
	e := &Exec{}
	// `cat` echoes stdin to stdout. The runner must pipe r in.
	res := e.RunWithStdin(context.Background(),
		strings.NewReader("pgsandbox"),
		"/bin/sh", "-c", "cat")
	if res.Err != nil {
		t.Fatalf("err: %v", res.Err)
	}
	if string(res.Stdout) != "pgsandbox" {
		t.Errorf("Stdout: got %q, want %q", res.Stdout, "pgsandbox")
	}
}

func TestRunContextCancellation(t *testing.T) {
	// Cancel mid-sleep; expect the runner to kill the child and
	// return a non-zero exit / non-nil err quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := (&Exec{}).Run(ctx, "/bin/sh", "-c", "sleep 5")
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("context cancellation took too long: %v", elapsed)
	}
	// We don't assert ExitCode == specific number — it varies by
	// platform (negative on macOS, 130-ish on Linux when SIGINT).
	// What we DO require: the child was not allowed to run to
	// completion.
	if res.ExitCode == 0 {
		t.Errorf("expected non-zero ExitCode after cancellation, got 0")
	}
}

func TestLocateExplicitPath(t *testing.T) {
	e := &Exec{}
	p, err := e.Locate("/bin/sh")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if p != "/bin/sh" {
		t.Errorf("Locate explicit path: got %q, want /bin/sh", p)
	}
}

func TestLocateBinDirWins(t *testing.T) {
	// Create a temp dir with a fake "psql" binary inside. Verify
	// Locate prefers it over PATH.
	dir := t.TempDir()
	fake := filepath.Join(dir, "psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	e := &Exec{BinDir: dir}
	got, err := e.Locate("psql")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != fake {
		t.Errorf("Locate: got %q, want BinDir copy %q", got, fake)
	}
}

func TestLocateFallsBackToPATH(t *testing.T) {
	// With no BinDir set, locating /bin/sh's basename should fall
	// back to PATH and find it. This test is implicit-PATH-aware:
	// `sh` is always in PATH on POSIX systems we support.
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	e := &Exec{}
	got, err := e.Locate("sh")
	if err != nil {
		t.Fatalf("Locate sh: %v", err)
	}
	if !strings.HasSuffix(got, "/sh") {
		t.Errorf("Locate sh: got %q, want absolute path ending /sh", got)
	}
}

func TestLocateMissing(t *testing.T) {
	e := &Exec{BinDir: t.TempDir()}
	_, err := e.Locate("definitely-not-a-real-binary-xyz")
	if err == nil {
		t.Fatal("Locate missing binary: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Locate error doesn't mention 'not found': %v", err)
	}
}

// Regression: a BinDir pointing at a path that doesn't exist
// (typical: PGS_BIN_DIR=/opt/postgresql/18.4 when the install on
// disk is 18.3) must surface as "bin-dir does not exist" rather
// than the more confusing "<name> not found in BinDir or PATH"
// wrap that used to hide the root cause.
func TestLocateBinDirDoesNotExist(t *testing.T) {
	e := &Exec{BinDir: filepath.Join(t.TempDir(), "definitely-not-here")}
	_, err := e.Locate("initdb")
	if err == nil {
		t.Fatal("Locate: want error for missing BinDir, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bin-dir does not exist") {
		t.Errorf("Locate error should name the missing bin-dir; got %q", msg)
	}
	if strings.Contains(msg, "PATH") {
		t.Errorf("Locate error for missing BinDir shouldn't mention PATH; got %q", msg)
	}
}

// UX: users naturally pass the install prefix (e.g.
// /opt/postgresql/18.3) rather than the bin/ subdir. Locate must
// find the binary under <BinDir>/bin/ when it isn't directly under
// <BinDir>, so deploy works whether the user wrote
// `--bin-dir /opt/postgresql/18.3` or
// `--bin-dir /opt/postgresql/18.3/bin`.
func TestLocateBinDirHasBinSubdir(t *testing.T) {
	dir := t.TempDir()
	binSub := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binSub, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	fake := filepath.Join(binSub, "initdb")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	e := &Exec{BinDir: dir}
	got, err := e.Locate("initdb")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != fake {
		t.Errorf("Locate: got %q, want bin/ fallback %q", got, fake)
	}
}

// Direct-in-BinDir wins over bin/ subdir: SPEC's documented
// semantics (BinDir IS the bin/ dir) keep priority even when both
// layouts exist. Guards against an accidental swap of the two
// checks.
func TestLocateBinDirDirectWinsOverBinSubdir(t *testing.T) {
	dir := t.TempDir()
	binSub := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binSub, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	direct := filepath.Join(dir, "initdb")
	if err := os.WriteFile(direct, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write direct: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binSub, "initdb"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write sub: %v", err)
	}
	e := &Exec{BinDir: dir}
	got, err := e.Locate("initdb")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != direct {
		t.Errorf("Locate: got %q, want direct %q (SPEC says BinDir IS the bin/ dir)", got, direct)
	}
}

// Regression: a BinDir that exists but isn't a directory must
// surface as "bin-dir is not a directory", not as a generic
// "<name> not found" error.
func TestLocateBinDirIsAFile(t *testing.T) {
	tmp := t.TempDir()
	notADir := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("write notadir: %v", err)
	}
	e := &Exec{BinDir: notADir}
	_, err := e.Locate("initdb")
	if err == nil {
		t.Fatal("Locate: want error for non-dir BinDir, got nil")
	}
	if !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("Locate error should call out non-directory; got %q", err)
	}
}

func TestRunLogsAtDebug(t *testing.T) {
	// SPEC §4.6: when --debug is on (Debug logger attached), every
	// external invocation emits one `# exec: <path> <args>` line to
	// the debug writer (defaults to os.Stderr; swapped out here so
	// the test can capture it). The literal `# exec: ` prefix is
	// grep-load-bearing and is asserted exactly.
	var buf bytes.Buffer
	orig := debugExecWriter
	debugExecWriter = &buf
	t.Cleanup(func() { debugExecWriter = orig })

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := (&Exec{}).WithLogger(log)
	_ = e.Run(context.Background(), "/bin/sh", "-c", "true")
	out := buf.String()
	if !strings.HasPrefix(out, "# exec: ") {
		t.Errorf("debug line missing literal `# exec: ` prefix: %q", out)
	}
	if !strings.Contains(out, "/bin/sh") {
		t.Errorf("debug line missing resolved path: %q", out)
	}
	if !strings.Contains(out, "-c true") {
		t.Errorf("debug line missing args: %q", out)
	}
}

func TestRunSilentWithoutDebugLogger(t *testing.T) {
	// An Info-level logger (default) MUST NOT produce the `# exec: `
	// line — the line is gated on the logger's Debug enablement so
	// --quiet and the no-flag default both stay quiet.
	var buf bytes.Buffer
	orig := debugExecWriter
	debugExecWriter = &buf
	t.Cleanup(func() { debugExecWriter = orig })

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := (&Exec{}).WithLogger(log)
	_ = e.Run(context.Background(), "/bin/sh", "-c", "true")
	if buf.Len() != 0 {
		t.Errorf("expected no debug output at Info level, got %q", buf.String())
	}
}

func TestWithLoggerChainable(t *testing.T) {
	// The WithLogger setter MUST return the receiver so callers can
	// chain it onto pgexec.New. Without this, the global-flags slice
	// has to break its idiomatic one-line construction at every call
	// site.
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := New("/tmp").WithLogger(log)
	if e.Logger != log {
		t.Errorf("WithLogger did not attach the logger")
	}
	if e.BinDir != "/tmp" {
		t.Errorf("WithLogger clobbered BinDir: got %q", e.BinDir)
	}
}

func TestExitCodeOfWrapping(t *testing.T) {
	// nil err → 0, nil
	code, err := exitCodeOf(nil)
	if code != 0 || err != nil {
		t.Errorf("nil: got (%d, %v); want (0, nil)", code, err)
	}
	// Synthetic non-ExitError → -1, err preserved.
	custom := errors.New("not-an-exit-error")
	code, err = exitCodeOf(custom)
	if code != -1 || !errors.Is(err, custom) {
		t.Errorf("custom err: got (%d, %v); want (-1, custom)", code, err)
	}
}

// Helper retained for future tests; silences unused-import lint
// for io across the file if it gets used later.
var _ = io.Discard
