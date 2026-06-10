// Tests for the `report` CLI handler. The real pg_gather pipeline
// is heavyweight (stand up a throwaway sandbox, ingest out.txt, run
// the gather scripts, render HTML); these tests cover the failure
// paths that fire BEFORE any of that work.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/ui"
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
	// Point install discovery at an empty dir so the auto-latest
	// fallback finds nothing and the usage error still fires — without
	// this the real /opt/postgresql on the dev machine would resolve.
	withDefaultInstallBase(t, t.TempDir())
	var stderr bytes.Buffer
	rc := runReport([]string{"--input", "/tmp/out.txt"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--bin-dir is required") {
		t.Errorf("stderr missing --bin-dir hint: %q", stderr.String())
	}
}

func TestRunReport_autoResolvesLatestInstall(t *testing.T) {
	// With no bin-dir anywhere, the dispatcher must discover the latest
	// install under defaultInstallBase instead of erroring. We stop the
	// pipeline at the next gate (pg-gather-dir) so this test stays
	// lightweight: getting ExitPgGatherDirMissing (not the --bin-dir
	// usage error) proves bin-dir resolution succeeded, and the INFO
	// line proves which install was chosen.
	t.Setenv("PGS_BIN_DIR", "")
	t.Setenv("PGS_PG_GATHER_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := t.TempDir()
	fakeInstall(t, base, "16.5")
	fakeInstall(t, base, "18.3")
	withDefaultInstallBase(t, base)

	var stderr bytes.Buffer
	rc := runReport([]string{"--input", "/tmp/out.txt"}, nil, &stderr)
	if rc != ui.ExitPgGatherDirMissing.Int() {
		t.Errorf("rc = %d, want %d (ExitPgGatherDirMissing); stderr=%q",
			rc, ui.ExitPgGatherDirMissing.Int(), stderr.String())
	}
	if strings.Contains(stderr.String(), "--bin-dir is required") {
		t.Errorf("bin-dir should have auto-resolved, got usage error: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "using latest install") ||
		!strings.Contains(stderr.String(), "18.3") {
		t.Errorf("stderr missing latest-install INFO line for 18.3: %q", stderr.String())
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

func TestRunReport_forceRejectedAsUnknown(t *testing.T) {
	// --force was dropped and stays dropped: the report command errors
	// fast on missing inputs with no prompt to suppress, so there's
	// nothing for --force to do. Failure cleanup is controlled by the
	// separate --destroy-on-failure flag instead (see below), NOT by
	// overloading --force. Verify Parse still rejects both --force
	// aliases with the standard "flag provided but not defined" shape.
	for _, alias := range []string{"--force", "-f"} {
		t.Run(alias, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runReport([]string{alias}, nil, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "flag provided but not defined") &&
				!strings.Contains(stderr.String(), "not defined") {
				t.Errorf("stderr does not look like an unknown-flag rejection: %q", stderr.String())
			}
		})
	}
}

func TestRunReport_destroyOnFailureAccepted(t *testing.T) {
	// --destroy-on-failure / -D is a real flag (it forces cleanup of the
	// throwaway sandbox when the report fails). Verify Parse ACCEPTS
	// both forms: with no --input, the command must fail for the
	// missing-input reason, NOT with an unknown-flag rejection.
	for _, alias := range []string{"--destroy-on-failure", "-D"} {
		t.Run(alias, func(t *testing.T) {
			var stderr bytes.Buffer
			rc := runReport([]string{alias}, nil, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if strings.Contains(stderr.String(), "not defined") {
				t.Errorf("%s was rejected as unknown; it should be accepted: %q", alias, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--input is required") {
				t.Errorf("expected missing-input error, got: %q", stderr.String())
			}
		})
	}
}
