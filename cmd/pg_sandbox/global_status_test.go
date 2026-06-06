// Tests for the `global_status` CLI handler. The walk logic lives in
// internal/sandbox; here we just exercise the dispatcher's flag
// surface and the happy path against an empty root (which is a
// supported state — no sandboxes is a legal result, not an error).

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

func TestRunGlobalStatus_emptyRootIsOK(t *testing.T) {
	// With --root pointing at an empty tmp dir, the walk returns
	// no sandboxes, the text renderer prints a "nothing here"
	// summary, and the handler exits 0. This pins the
	// "no sandboxes is not an error" SPEC §6.12 contract.
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runGlobalStatus([]string{"--root", tmp}, &stdout, &stderr)
	if rc != ui.ExitOK.Int() {
		t.Errorf("rc = %d, want %d; stderr=%q", rc, ui.ExitOK.Int(), stderr.String())
	}
	// Output sanity: stdout should have *something* on it
	// (RenderText emits at least a header line); we don't pin the
	// exact wording to avoid coupling to render details.
	if stdout.Len() == 0 {
		t.Errorf("stdout empty on empty-root happy path")
	}
}

func TestRunGlobalStatus_jsonProducesArrayOrObjectOnStdout(t *testing.T) {
	// SPEC §6.12 says --json emits machine-readable JSON. For an
	// empty root, the exact shape (empty array vs empty object) is
	// the package's concern; we just pin that it starts with one of
	// the JSON-object-or-array delimiters.
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runGlobalStatus([]string{"--json", "--root", tmp}, &stdout, &stderr)
	if rc != ui.ExitOK.Int() {
		t.Errorf("rc = %d, want %d; stderr=%q", rc, ui.ExitOK.Int(), stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(out, "{") && !strings.HasPrefix(out, "[") {
		t.Errorf("--json stdout doesn't start with { or [: %q", out)
	}
}

func TestRunGlobalStatus_invalidFlagIsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runGlobalStatus([]string{"--not-a-real-flag"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("stderr doesn't surface the bad flag: %q", stderr.String())
	}
}

func TestRunGlobalStatus_debugQuietMutex(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runGlobalStatus([]string{"--debug", "--quiet", "--root", tmp}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}
