// Tests for the `status` CLI handler. These exercise the failure
// paths that fire BEFORE any PostgreSQL process is spawned: missing
// --sandbox-dir, invalid flags, --debug/--quiet mutual exclusion, and
// the not-a-sandbox branch. The happy path runs a real psql probe
// against a running cluster, which is out of scope for a unit test
// in the cmd package (covered at the integration tier).

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

func TestRunStatus_missingSandboxDirIsUsage(t *testing.T) {
	// SPEC §6.4: --sandbox-dir is required. Empty argv → ExitUsage,
	// stderr mentions the required flag and points at `help status`.
	var stdout, stderr bytes.Buffer
	rc := runStatus(nil, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on usage error, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "help status") {
		t.Errorf("stderr missing 'help status' hint: %q", stderr.String())
	}
}

func TestRunStatus_invalidFlagIsUsage(t *testing.T) {
	// An unknown flag must trigger ExitUsage with the flag name in
	// the FlagSet's "flag provided but not defined" diagnostic. This
	// is stdlib `flag` behavior — we pin it so a future swap to a
	// different parser preserves the user-visible contract.
	var stdout, stderr bytes.Buffer
	rc := runStatus([]string{"--not-a-real-flag"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("stderr doesn't surface the bad flag name: %q", stderr.String())
	}
}

func TestRunStatus_helpFlagEmitsUsageToStderr(t *testing.T) {
	// When --help reaches the handler directly (e.g. through the
	// re-prepended global-flag path) stdlib `flag` writes its
	// auto-generated usage block to stderr and returns its
	// `flag.ErrHelp` sentinel. Our handler maps that to ExitUsage.
	//
	// This is distinct from the dispatcher's `<cmd> --help`
	// interception path (tested in help_test.go), which writes the
	// rich block to stdout and returns 0. Both are intentional —
	// the rich block is the public contract; the FlagSet's terse
	// block is the safety net.
	var stdout, stderr bytes.Buffer
	rc := runStatus([]string{"--help"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d (--help via handler returns ErrHelp → ExitUsage)", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "Usage of status") {
		t.Errorf("stderr missing FlagSet usage banner: %q", stderr.String())
	}
}

func TestRunStatus_notASandboxIsExitNotASandbox(t *testing.T) {
	// A real-but-empty dir → ExitNotASandbox. This is the gate that
	// stops `pg_sandbox status -s /tmp` from getting interpreted as
	// "/tmp is a sandbox".
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runStatus([]string{"--sandbox-dir", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d (ExitNotASandbox)", rc, ui.ExitNotASandbox.Int())
	}
	if !strings.Contains(stderr.String(), "not a sandbox") {
		t.Errorf("stderr missing 'not a sandbox': %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), tmp) {
		t.Errorf("stderr doesn't surface the bad dir %q: %q", tmp, stderr.String())
	}
}

func TestRunStatus_debugQuietMutexFailsBeforeWork(t *testing.T) {
	// SPEC §5: --debug and --quiet are mutually exclusive. The
	// rejection must fire BEFORE the sandbox check so an
	// unreachable / nonexistent sandbox dir doesn't change the exit
	// code. Pin both placements (before / after) so a future
	// reorder doesn't regress the combo detection on one path.
	cases := []struct {
		name string
		args []string
	}{
		{"globals before sandbox", []string{"--debug", "--quiet", "--sandbox-dir", "/nonexistent/sandbox"}},
		{"globals after sandbox", []string{"--sandbox-dir", "/nonexistent/sandbox", "--debug", "--quiet"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := runStatus(tc.args, &stdout, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d (ExitUsage for mutex)", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "mutually exclusive") {
				t.Errorf("stderr missing mutex marker: %q", stderr.String())
			}
		})
	}
}

func TestRunStatus_jsonFlagAcceptedAtParse(t *testing.T) {
	// We can't easily reach the happy `--json` branch (it requires a
	// running cluster + psql), but we CAN prove `--json` is wired
	// into the FlagSet by combining it with the not-a-sandbox dir
	// short-circuit: if --json weren't accepted, rc would be 2
	// (ExitUsage from "flag provided but not defined: -json"). With
	// it accepted, the handler reaches the sandbox check and returns
	// 3 (ExitNotASandbox). That gap is the proof.
	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runStatus([]string{"--json", "--sandbox-dir", tmp}, &stdout, &stderr)
	if rc != ui.ExitNotASandbox.Int() {
		t.Errorf("rc = %d, want %d (--json must be accepted so we reach IsSandboxDir)", rc, ui.ExitNotASandbox.Int())
	}
}

func TestRunStatus_jsonProducesObjectOnStdoutForMarkedSandbox(t *testing.T) {
	// Stronger happy-path proof for --json: a *minimally marked*
	// sandbox dir (just an empty `{}` pg_sandbox.json) gets past
	// IsSandboxDir AND LoadSandbox AND StatusWithStderr's "is
	// pg running?" probe (which returns state=stopped without
	// erroring when there's no postmaster.pid). The renderer then
	// writes the resolved StatusReport as JSON to stdout, which is
	// the surface we're pinning here.
	//
	// We're not asserting any field values — just that --json was
	// accepted, rc is OK, and the stdout payload begins with `{`
	// (the SPEC-mandated JSON object shape).
	tmp := t.TempDir()
	marker := filepath.Join(tmp, config.SandboxFilename)
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	rc := runStatus([]string{"--json", "--sandbox-dir", tmp}, &stdout, &stderr)
	if rc != ui.ExitOK.Int() {
		t.Errorf("rc = %d, want %d; stderr=%q", rc, ui.ExitOK.Int(), stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(out, "{") {
		t.Errorf("--json stdout doesn't start with '{': %q", out)
	}
	if !strings.HasSuffix(out, "}") {
		t.Errorf("--json stdout doesn't end with '}': %q", out)
	}
}
