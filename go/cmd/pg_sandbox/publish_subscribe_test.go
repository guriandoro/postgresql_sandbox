// Tests for the `publish`, `subscribe`, and `promote` CLI handlers.
// Grouped because they share the same shape: thin wrappers around an
// internal/sandbox function with --sandbox-dir as the load-bearing
// required flag. We cover the failure paths that fire before any
// psql/pg_dump/pg_ctl invocation.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// -----------------------------------------------------------------
// publish
// -----------------------------------------------------------------

func TestRunPublish_missingRequiredFlagsIsUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"only sandbox-dir", []string{"-s", "/tmp/x"}},
		{"only pub-name", []string{"--pub-name", "p1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runPublish(tc.args, nil, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "are required") {
				t.Errorf("stderr missing required-flag message: %q", stderr.String())
			}
		})
	}
}

func TestRunPublish_allTablesAndTablesMutuallyExclusive(t *testing.T) {
	// Exactly one of --all-tables / --tables. Both set or both
	// unset → ExitUsage with a clear message.
	cases := []struct {
		name string
		args []string
	}{
		{"both set", []string{"-s", "/tmp/x", "--pub-name", "p1", "--all-tables", "--tables", "t1"}},
		{"neither set", []string{"-s", "/tmp/x", "--pub-name", "p1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runPublish(tc.args, nil, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "exactly one of --all-tables or --tables") {
				t.Errorf("stderr missing mutex/required message: %q", stderr.String())
			}
		})
	}
}

func TestRunPublish_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runPublish([]string{"-s", tmp, "--pub-name", "p1", "--all-tables"}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunPublish_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runPublish([]string{"--bogus"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunPublish_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runPublish([]string{"--debug", "--quiet", "-s", "/x", "--pub-name", "p", "--all-tables"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

// -----------------------------------------------------------------
// subscribe
// -----------------------------------------------------------------

func TestRunSubscribe_missingRequiredFlagsIsUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"only sandbox-dir", []string{"-s", "/tmp/x"}},
		{"missing pub-name", []string{"-s", "/tmp/x", "--from", "pub"}},
		{"missing from", []string{"-s", "/tmp/x", "--pub-name", "p"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runSubscribe(tc.args, nil, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "are required") {
				t.Errorf("stderr missing required-flag message: %q", stderr.String())
			}
		})
	}
}

func TestRunSubscribe_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runSubscribe([]string{"-s", tmp, "--from", "pub", "--pub-name", "p"}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunSubscribe_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runSubscribe([]string{"--bogus"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunSubscribe_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runSubscribe([]string{"--debug", "--quiet", "-s", "/x", "--from", "p", "--pub-name", "n"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

func TestRunSubscribe_optionalFlagsAcceptedAtParse(t *testing.T) {
	// --copy-schema, --no-copy-data, --sub-name, --dbname all
	// optional. Verify Parse accepts them by combining with a
	// not-a-sandbox dir; rc=3 means Parse passed.
	tmp := t.TempDir()
	args := []string{
		"-s", tmp, "--from", "pub", "--pub-name", "p1",
		"--sub-name", "s1", "--copy-schema", "--no-copy-data",
		"-d", "appdb",
	}
	var stderr bytes.Buffer
	rc := runSubscribe(args, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d (optional flags must be accepted)", rc, ui.ExitNotASandbox.Int())
	}
}

// -----------------------------------------------------------------
// promote
// -----------------------------------------------------------------

func TestRunPromote_missingSandboxDirIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runPromote(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
}

func TestRunPromote_notASandboxIsExitNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runPromote([]string{"-s", tmp}, nil, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunPromote_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runPromote([]string{"--bogus"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunPromote_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runPromote([]string{"--debug", "--quiet", "-s", "/x"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}
