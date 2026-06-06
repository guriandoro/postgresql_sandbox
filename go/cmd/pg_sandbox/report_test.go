// Tests for the `report` CLI handler. The real pg_gather pipeline
// is heavyweight (stand up a throwaway sandbox, ingest out.txt, run
// the gather scripts, render HTML); these tests cover the failure
// paths that fire BEFORE any of that work.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func TestRunReport_missingInputIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runReport(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--input is required") {
		t.Errorf("stderr missing --input message: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "help report") {
		t.Errorf("stderr missing 'help report' hint: %q", stderr.String())
	}
}

func TestRunReport_missingBinDirIsUsage(t *testing.T) {
	t.Setenv("PGS_BIN_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from global config
	var stderr bytes.Buffer
	rc := runReport([]string{"--input", "/tmp/out.txt"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--bin-dir is required") {
		t.Errorf("stderr missing --bin-dir hint: %q", stderr.String())
	}
}

func TestRunReport_missingGatherDirIsExitPgGatherDirMissing(t *testing.T) {
	// SPEC §6.13: gather-dir missing has its own dedicated exit
	// code, not the generic ExitUsage. We force --bin-dir via env
	// to bypass the bin-dir check.
	t.Setenv("PGS_BIN_DIR", "/opt/pg")
	t.Setenv("PGS_PG_GATHER_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stderr bytes.Buffer
	rc := runReport([]string{"--input", "/tmp/out.txt"}, nil, &stderr)
	if rc != ui.ExitPgGatherDirMissing.Int() {
		t.Errorf("rc = %d, want %d (ExitPgGatherDirMissing)", rc, ui.ExitPgGatherDirMissing.Int())
	}
	if !strings.Contains(stderr.String(), "--pg-gather-dir is required") {
		t.Errorf("stderr missing --pg-gather-dir hint: %q", stderr.String())
	}
}

func TestRunReport_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runReport([]string{"--not-a-real-flag"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("stderr doesn't surface the bad flag: %q", stderr.String())
	}
}

func TestRunReport_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runReport([]string{"--debug", "--quiet", "--input", "/tmp/x"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

func TestRunReport_forceAcceptedAtParse(t *testing.T) {
	// --force / -f are accepted but currently unused (reserved for
	// prompt suppression in a later slice). Verify Parse accepts both
	// aliases by combining with the missing-input path: rc=2 with
	// "--input is required" means Parse accepted -f without rejecting
	// it as an unknown flag.
	for _, alias := range []string{"--force", "-f"} {
		t.Run(alias, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runReport([]string{alias}, nil, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "--input is required") {
				t.Errorf("stderr doesn't reach the --input check (parse rejection?): %q", stderr.String())
			}
		})
	}
}
