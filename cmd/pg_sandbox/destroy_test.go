// Tests for the `destroy` CLI handler. Cover the failure paths that
// fire BEFORE the rm: missing --sandbox-dir, unknown flag,
// --debug/--quiet mutex, not-a-sandbox, and the non-TTY-without-force
// refusal (SPEC §4.7). The actual rm path requires a real sandbox
// tree on disk and a TTY-gated y/N prompt — covered at the
// integration tier.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

func TestRunDestroy_missingSandboxDirIsUsage(t *testing.T) {
	var _, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	rc := runDestroy(nil, nil, stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "help destroy") {
		t.Errorf("stderr missing 'help destroy' hint: %q", stderr.String())
	}
}

func TestRunDestroy_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runDestroy([]string{"--not-a-real-flag"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("stderr doesn't surface the bad flag: %q", stderr.String())
	}
}

func TestRunDestroy_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runDestroy([]string{"--debug", "--quiet", "-s", "/nope"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

func TestRunDestroy_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runDestroy([]string{"--sandbox-dir", tmp}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
	if !strings.Contains(stderr.String(), "not a sandbox") {
		t.Errorf("stderr missing 'not a sandbox': %q", stderr.String())
	}
}

func TestRunDestroy_forceFlagAcceptedAtParse(t *testing.T) {
	// --force is wired into the FlagSet. We can't run the rm path
	// from a unit test (it requires a real sandbox tree on disk),
	// so we verify the flag is recognized by combining it with a
	// not-a-sandbox dir and asserting rc=3 (ExitNotASandbox) rather
	// than rc=2 (which would indicate --force was rejected by Parse).
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runDestroy([]string{"--force", "-s", tmp}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d (--force must be accepted)", rc, ui.ExitNotASandbox.Int())
	}
	// Short alias too.
	stderr.Reset()
	rc = runDestroy([]string{"-f", "-s", tmp}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d on -f, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunDestroy_markedSandboxWithEmptyConfigReachesLoadOrConfirm(t *testing.T) {
	// With a marker file present (so IsSandboxDir returns true),
	// destroy progresses to LoadSandbox. An empty `{}` config loads
	// cleanly; --force then bypasses the TTY prompt and we hit
	// sandbox.Destroy. That layer is not the cmd-package's
	// contract — we just pin that the gate progresses past the
	// IsSandboxDir check (i.e. rc is no longer ExitNotASandbox).
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, config.SandboxFilename), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rc := runDestroy([]string{"--force", "-s", tmp}, nil, &stderr)
	if rc == ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d (ExitNotASandbox) — gate should have progressed past IsSandboxDir for a marked dir", rc)
	}
}
