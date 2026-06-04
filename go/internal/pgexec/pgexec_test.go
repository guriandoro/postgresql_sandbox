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

func TestRunLogsAtDebug(t *testing.T) {
	// When Logger is set, Run emits a debug line with the exec
	// path and args. Verify the prefix and args are visible.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := &Exec{Logger: log}
	_ = e.Run(context.Background(), "/bin/sh", "-c", "true")
	out := buf.String()
	if !strings.Contains(out, "exec") {
		t.Errorf("debug log missing 'exec' tag: %q", out)
	}
	if !strings.Contains(out, "/bin/sh") {
		t.Errorf("debug log missing resolved path: %q", out)
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
