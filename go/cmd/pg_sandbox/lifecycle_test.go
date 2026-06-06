// Tests for the start / stop / restart handlers and the shared
// lifecycleCommand helper they delegate to. Same failure paths as
// status (missing flag, not-a-sandbox, invalid flag, mutex) but
// across the three lifecycle commands.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// The tests below repeat the per-command failure-path shape three
// times instead of building a generic table — the run* signatures
// take io.Writer (not a generic-typed parameter) and Go's generics
// don't usefully reduce the repetition. Three short copies are
// easier to read than a clever abstraction.

func TestRunStart_failurePaths(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantRc    int
		wantInStd string
	}{
		{"missing sandbox-dir", nil, ui.ExitUsage.Int(), "--sandbox-dir is required"},
		{"invalid flag", []string{"--bogus"}, ui.ExitUsage.Int(), "bogus"},
		{"debug+quiet", []string{"--debug", "--quiet", "-s", "/x"}, ui.ExitUsage.Int(), "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := runStart(tc.args, &stdout, &stderr)
			if rc != tc.wantRc {
				t.Errorf("rc = %d, want %d", rc, tc.wantRc)
			}
			if !strings.Contains(stderr.String(), tc.wantInStd) {
				t.Errorf("stderr missing %q: %q", tc.wantInStd, stderr.String())
			}
		})
	}
}

func TestRunStart_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runStart([]string{"-s", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
	if !strings.Contains(stderr.String(), "not a sandbox") {
		t.Errorf("stderr missing 'not a sandbox': %q", stderr.String())
	}
}

func TestRunStop_failurePaths(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantRc    int
		wantInStd string
	}{
		{"missing sandbox-dir", nil, ui.ExitUsage.Int(), "--sandbox-dir is required"},
		{"invalid flag", []string{"--bogus"}, ui.ExitUsage.Int(), "bogus"},
		{"debug+quiet", []string{"--debug", "--quiet", "-s", "/x"}, ui.ExitUsage.Int(), "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := runStop(tc.args, &stdout, &stderr)
			if rc != tc.wantRc {
				t.Errorf("rc = %d, want %d", rc, tc.wantRc)
			}
			if !strings.Contains(stderr.String(), tc.wantInStd) {
				t.Errorf("stderr missing %q: %q", tc.wantInStd, stderr.String())
			}
		})
	}
}

func TestRunStop_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runStop([]string{"-s", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunRestart_failurePaths(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantRc    int
		wantInStd string
	}{
		{"missing sandbox-dir", nil, ui.ExitUsage.Int(), "--sandbox-dir is required"},
		{"invalid flag", []string{"--bogus"}, ui.ExitUsage.Int(), "bogus"},
		{"debug+quiet", []string{"--debug", "--quiet", "-s", "/x"}, ui.ExitUsage.Int(), "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := runRestart(tc.args, &stdout, &stderr)
			if rc != tc.wantRc {
				t.Errorf("rc = %d, want %d", rc, tc.wantRc)
			}
			if !strings.Contains(stderr.String(), tc.wantInStd) {
				t.Errorf("stderr missing %q: %q", tc.wantInStd, stderr.String())
			}
		})
	}
}

func TestRunRestart_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runRestart([]string{"-s", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestLifecycle_perCommandUsageHintIsCorrect(t *testing.T) {
	// The shared lifecycleCommand helper writes "help <name>" with the
	// name it was constructed with — start/stop/restart each must
	// surface its own name in the usageHint, not a shared placeholder.
	var stdout, stderr bytes.Buffer

	stderr.Reset()
	_ = runStart(nil, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "help start") {
		t.Errorf("start usage hint wrong: %q", stderr.String())
	}
	stderr.Reset()
	_ = runStop(nil, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "help stop") {
		t.Errorf("stop usage hint wrong: %q", stderr.String())
	}
	stderr.Reset()
	_ = runRestart(nil, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "help restart") {
		t.Errorf("restart usage hint wrong: %q", stderr.String())
	}
}
