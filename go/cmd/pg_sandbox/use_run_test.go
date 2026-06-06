// Tests for the `use` and `run` CLI handlers. Both are exec-style
// commands; we cover only the pre-exec failure paths (missing
// sandbox-dir, missing binary arg for run, not-a-sandbox, mutex,
// invalid flag). The success branch hands off to syscall.Exec
// which is unreachable from a hosted Go test.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// -----------------------------------------------------------------
// use
// -----------------------------------------------------------------

func TestRunUse_missingSandboxDirIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runUse(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
}

func TestRunUse_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runUse([]string{"-s", tmp}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunUse_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runUse([]string{"--bogus"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunUse_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runUse([]string{"--debug", "--quiet", "-s", "/x"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

// -----------------------------------------------------------------
// run
// -----------------------------------------------------------------

func TestRunRun_missingSandboxDirIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runRun(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
}

func TestRunRun_missingBinaryIsUsage(t *testing.T) {
	// `run -s X` without a binary positional → ExitUsage with a
	// "missing binary name" message. We use a tmp dir (which won't be
	// a sandbox) — but the binary check fires BEFORE the sandbox
	// check, so we see the missing-binary message instead of
	// not-a-sandbox.
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runRun([]string{"-s", tmp}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "missing binary name") {
		t.Errorf("stderr missing 'missing binary name': %q", stderr.String())
	}
}

func TestRunRun_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	// With -s, a binary positional, and a bogus dir: not-a-sandbox
	// fires after the binary-name check.
	rc := runRun([]string{"-s", tmp, "psql"}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunRun_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runRun([]string{"--bogus"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunRun_noDsnAliasesAcceptedAtParse(t *testing.T) {
	// --no-dsn and -n must both reach the FlagSet. We combine with a
	// not-a-sandbox dir to confirm Parse passed; rc=3 means the flag
	// surface was accepted.
	tmp := t.TempDir()
	for _, alias := range []string{"--no-dsn", "-n"} {
		t.Run(alias, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runRun([]string{"-s", tmp, alias, "psql"}, nil, &stderr)
			if rc != ui.ExitNotASandbox.Int() {
				t.Errorf("rc = %d, want %d (%s must be accepted)", rc, ui.ExitNotASandbox.Int(), alias)
			}
		})
	}
}

func TestRunRun_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runRun([]string{"--debug", "--quiet", "-s", "/x", "psql"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}
