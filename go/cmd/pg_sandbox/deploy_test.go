// Tests for the `deploy` CLI handler. These cover the failure paths
// that fire BEFORE initdb / pg_ctl run: missing --sandbox-dir,
// missing --bin-dir, --debug/--quiet mutex, and the bool/string flag
// surface (--replicate-from, --slot, --subscribe-to, --pub-name,
// --copy-schema, --no-copy-data). The successful deploy path requires
// real initdb + pg_ctl execution and is covered at the integration tier.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func TestRunDeploy_missingSandboxDirIsUsage(t *testing.T) {
	// Belt-and-suspenders for the env layer: even with PGS_BIN_DIR
	// set, missing --sandbox-dir is still ExitUsage. We explicitly
	// clear PGS_BIN_DIR so the test isn't dependent on the harness
	// shell's env.
	t.Setenv("PGS_BIN_DIR", "/opt/pg")
	var stderr bytes.Buffer
	rc := runDeploy(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--sandbox-dir is required") {
		t.Errorf("stderr missing required-flag message: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "help deploy") {
		t.Errorf("stderr missing 'help deploy' hint: %q", stderr.String())
	}
}

func TestRunDeploy_missingBinDirIsUsage(t *testing.T) {
	// --bin-dir is required (or PGS_BIN_DIR env). With both unset
	// and --sandbox-dir supplied, the handler must error with a
	// clear "set PGS_BIN_DIR" hint.
	t.Setenv("PGS_BIN_DIR", "")
	tmp := t.TempDir()
	var stderr bytes.Buffer
	rc := runDeploy([]string{"--sandbox-dir", tmp}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "--bin-dir is required") {
		t.Errorf("stderr missing --bin-dir hint: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "PGS_BIN_DIR") {
		t.Errorf("stderr missing env-var hint: %q", stderr.String())
	}
}

func TestRunDeploy_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runDeploy([]string{"--not-a-real-flag"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("stderr doesn't surface the bad flag: %q", stderr.String())
	}
}

func TestRunDeploy_debugQuietMutex(t *testing.T) {
	t.Setenv("PGS_BIN_DIR", "/opt/pg")
	var stderr bytes.Buffer
	rc := runDeploy([]string{"--debug", "--quiet", "-s", "/nope", "-b", "/opt/pg"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

func TestRunDeploy_allFlagsAcceptedAtParse(t *testing.T) {
	// Verify the documented flag surface is wired into the FlagSet.
	// We pass every flag with a representative value and assert the
	// handler proceeds past Parse (i.e. doesn't return ExitUsage with
	// a "flag provided but not defined" message). The actual deploy
	// will fail downstream — fine; we only need to know Parse
	// accepted the surface.
	//
	// NOTE: --replicate-from and --subscribe-to are mutually exclusive
	// inside sandbox.Deploy, so we pass them in two separate groups.
	tmp := t.TempDir()
	cases := [][]string{
		// Physical replication flag group + every general flag.
		{
			"-s", tmp,
			"-b", "/opt/postgresql/18.4",
			"--host", "127.0.0.1",
			"-p", "54321",
			"-U", "postgres",
			"-d", "postgres",
			"--data-dir", "data",
			"--log", "server.log",
			"--replicate-from", "src",
			"--slot", "myslot",
		},
		// Logical replication flag group.
		{
			"-s", tmp,
			"-b", "/opt/postgresql/18.4",
			"--subscribe-to", "pub",
			"--pub-name", "mypub",
			"--sub-name", "mysub",
			"--copy-schema",
			"--no-copy-data",
		},
	}
	for i, args := range cases {
		var stderr bytes.Buffer
		_ = runDeploy(args, nil, &stderr)
		// The contract this test pins is "no flag was rejected by
		// Parse". A Parse rejection prints "flag provided but not
		// defined: -<name>"; we assert that line is NOT present.
		// Domain errors from sandbox.Deploy (e.g. bad bin-dir) are
		// fine — they prove Parse accepted the flag surface.
		if strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Errorf("case %d: a flag was rejected by Parse — surface drift?\nargs=%v\nstderr=%q", i, args, stderr.String())
		}
	}
}

func TestFirstNonEmpty_helper(t *testing.T) {
	// firstNonEmpty is the deploy-side resolution tiebreaker. Cover
	// the three branches at unit level so a regression in it (used
	// for every per-flag override choice) gets caught here, not in a
	// downstream integration failure.
	if got := firstNonEmpty("a", "b", "c"); got != "a" {
		t.Errorf("first non-empty: got %q, want a", got)
	}
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Errorf("skip first empty: got %q, want b", got)
	}
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Errorf("all empty: got %q, want \"\"", got)
	}
}

func TestPortOrEnv_helper(t *testing.T) {
	// portOrEnv picks between the explicit-flag value, the env value,
	// and 0 (auto-allocate). The contract is:
	//   - explicit flag wins (even if 0)
	//   - else env wins if non-zero
	//   - else 0
	if got := portOrEnv(54321, true, 99999); got != 54321 {
		t.Errorf("explicit flag should win: got %d", got)
	}
	if got := portOrEnv(0, true, 99999); got != 0 {
		t.Errorf("explicit 0 should still win: got %d", got)
	}
	if got := portOrEnv(0, false, 12345); got != 12345 {
		t.Errorf("env should win when flag not set: got %d", got)
	}
	if got := portOrEnv(0, false, 0); got != 0 {
		t.Errorf("default 0: got %d", got)
	}
}
