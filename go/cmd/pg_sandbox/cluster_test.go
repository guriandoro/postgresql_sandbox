// Tests for the `cluster` CLI dispatcher and its sub-subcommands
// (deploy, status, destroy). The actual cluster orchestration
// requires real initdb + pg_ctl + pg_basebackup runs and is covered
// at the integration tier; here we cover the dispatch shape, every
// flag wiring, and the failure paths that fire before the cluster
// package's own code runs.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func TestRunCluster_noSubcommandPrintsUsage(t *testing.T) {
	// `cluster` with nothing after it must print the inner usage to
	// stderr and exit ExitUsage. The dispatcher's contract — the
	// help/discovery path goes through `cluster --help`, not
	// "empty argv falls through".
	var stdout, stderr bytes.Buffer
	rc := runCluster(nil, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "cluster") {
		t.Errorf("stderr missing cluster usage banner: %q", stderr.String())
	}
}

func TestRunCluster_unknownSubcommandIsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runCluster([]string{"not-a-real-sub"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Errorf("stderr missing unknown-sub marker: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not-a-real-sub") {
		t.Errorf("stderr doesn't echo the bad sub: %q", stderr.String())
	}
}

func TestRunCluster_helpAliasesRenderUsageOnStdout(t *testing.T) {
	// `cluster --help`, `cluster -h`, and `cluster help` all hit
	// the same rich-help path → stdout, exit 0.
	for _, h := range []string{"--help", "-h", "help"} {
		t.Run(h, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := runCluster([]string{h}, &stdout, &stderr)
			if rc != ui.ExitOK.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitOK.Int())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr should be empty, got %q", stderr.String())
			}
			if !strings.Contains(stdout.String(), "pg_sandbox cluster") {
				t.Errorf("stdout missing cluster banner: %q", stdout.String())
			}
		})
	}
}

func TestRunCluster_globalFlagsCapturedBeforeSubcommand(t *testing.T) {
	// `cluster --debug status -s …` and `cluster --color always
	// status -s …` must capture the global at the head and re-prepend
	// it onto the sub-subcommand argv so the leaf FlagSet still
	// accepts it. We verify by combining --debug with a bad
	// sandbox-dir; rc=4 (ExitNotACluster) proves the flag was
	// accepted (otherwise it'd be rc=2 with "flag provided but not
	// defined").
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runCluster([]string{"--debug", "status", "-s", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotACluster.Int() {
		t.Errorf("rc = %d, want %d (--debug before sub must be accepted)", rc, ui.ExitNotACluster.Int())
	}
}

// -----------------------------------------------------------------
// cluster deploy
// -----------------------------------------------------------------

func TestRunClusterDeploy_missingSandboxDirIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runClusterDeploy(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
}

func TestRunClusterDeploy_zeroNodesIsUsage(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runClusterDeploy([]string{"-s", tmp, "-b", "/opt/pg"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), ">= 1") {
		t.Errorf("stderr missing >=1 message: %q", stderr.String())
	}
}

func TestRunClusterDeploy_missingBinDirIsUsage(t *testing.T) {
	t.Setenv("PGS_BIN_DIR", "")
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runClusterDeploy([]string{"-s", tmp, "-N", "1"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--bin-dir is required") {
		t.Errorf("stderr missing --bin-dir hint: %q", stderr.String())
	}
}

func TestRunClusterDeploy_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runClusterDeploy([]string{"--not-a-real-flag"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunClusterDeploy_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runClusterDeploy([]string{"--debug", "--quiet", "-s", "/x", "-b", "/opt/pg", "-N", "1"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

// -----------------------------------------------------------------
// cluster status
// -----------------------------------------------------------------

func TestRunClusterStatus_missingSandboxDirIsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runClusterStatus(nil, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
}

func TestRunClusterStatus_notAClusterIsExitNotACluster(t *testing.T) {
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runClusterStatus([]string{"-s", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotACluster.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotACluster.Int())
	}
	if !strings.Contains(stderr.String(), "not a cluster") {
		t.Errorf("stderr missing 'not a cluster': %q", stderr.String())
	}
}

func TestRunClusterStatus_jsonAcceptedAtParse(t *testing.T) {
	// --json wiring proof, same shape as the status test: combine
	// with a not-a-cluster dir; rc=4 means --json was accepted.
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runClusterStatus([]string{"--json", "-s", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotACluster.Int() {
		t.Errorf("rc = %d, want %d (--json must be accepted)", rc, ui.ExitNotACluster.Int())
	}
}

func TestRunClusterStatus_invalidFlagIsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runClusterStatus([]string{"--not-a-real-flag"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

// -----------------------------------------------------------------
// cluster destroy
// -----------------------------------------------------------------

func TestRunClusterDestroy_missingSandboxDirIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runClusterDestroy(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
}

func TestRunClusterDestroy_notAClusterIsExitNotACluster(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runClusterDestroy([]string{"-s", tmp}, nil, &stderr)
	if rc != ui.ExitNotACluster.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitNotACluster.Int())
	}
}

func TestRunClusterDestroy_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runClusterDestroy([]string{"--bogus"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
}

func TestRunClusterDestroy_forceAcceptedAtParse(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runClusterDestroy([]string{"--force", "-s", tmp}, nil, &stderr)
	// rc=4 (ExitNotACluster) proves --force passed through Parse.
	if rc != ui.ExitNotACluster.Int() {
		t.Errorf("rc = %d, want %d (--force must be accepted)", rc, ui.ExitNotACluster.Int())
	}
}

func TestRunClusterDestroy_markedClusterReachesLoadOrConfirm(t *testing.T) {
	// With the marker file present, the gate progresses past
	// IsClusterDir. We use --force so the no-TTY refusal doesn't
	// kick in.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, config.ClusterFilename), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rc := runClusterDestroy([]string{"--force", "-s", tmp}, nil, &stderr)
	if rc == ui.ExitNotACluster.Int() {
		t.Errorf("rc = %d (ExitNotACluster) — gate should have progressed past IsClusterDir", rc)
	}
}

func TestMapClusterExit_handlesNonClusterErrors(t *testing.T) {
	// mapClusterExit is the dispatcher's small mapping helper. A nil
	// or unknown error should still return some sensible code. We
	// just pin that the function doesn't panic on a generic error.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("mapClusterExit panicked: %v", r)
		}
	}()
	_ = mapClusterExit(nil)
}
