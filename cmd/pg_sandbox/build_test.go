// Tests for the `build` CLI handler. The real build path is
// minutes-long (download, configure, make) and out of scope here;
// these tests cover the failure surface that fires before any
// invocation of build.Build: missing version positional, multiple
// versions, --debug/--quiet mutex, and the bool-flag-after-positional
// reorder integration that argv.go promises.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

func TestRunBuild_missingVersionIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runBuild(nil, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "<version> is required") {
		t.Errorf("stderr missing required-version message: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "help build") {
		t.Errorf("stderr missing 'help build' hint: %q", stderr.String())
	}
}

func TestRunBuild_multipleVersionsIsUsage(t *testing.T) {
	// SPEC §7.1: only one version per call. We refuse early rather
	// than try to be helpful (parallel builds would require a much
	// bigger contract).
	var stderr bytes.Buffer
	rc := runBuild([]string{"18.4", "17.2"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "only one version") {
		t.Errorf("stderr missing one-version message: %q", stderr.String())
	}
}

func TestRunBuild_invalidFlagIsUsage(t *testing.T) {
	var stderr bytes.Buffer
	rc := runBuild([]string{"--not-a-real-flag", "18.4"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "not-a-real-flag") {
		t.Errorf("stderr doesn't surface the bad flag: %q", stderr.String())
	}
}

func TestRunBuild_debugQuietMutex(t *testing.T) {
	var stderr bytes.Buffer
	rc := runBuild([]string{"--debug", "--quiet", "18.4"}, nil, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutex marker: %q", stderr.String())
	}
}

func TestRunBuild_versionThenBinDirNotTreatedAsSecondVersion(t *testing.T) {
	var stderr bytes.Buffer
	rc := runBuild([]string{"99.99", "-b", "/opt/postgresql"}, nil, &stderr)
	_ = rc
	if strings.Contains(stderr.String(), "only one version may be built at a time") {
		t.Errorf("-b after version regressed to positional; stderr=%q", stderr.String())
	}
}

func TestRunBuild_versionThenForceParsesAsFlagThenPositional(t *testing.T) {
	// The headline reorder contract from argv.go: a positional
	// version followed by a bool flag (`build 18.4 --force`) must
	// parse with --force seen by the FlagSet, not treated as a
	// second positional. If reorder were silently disabled, the
	// "only one version may be built" path would fire and we'd see
	// rc=2 with that message. We assert the message is NOT present.
	//
	// We use a deliberately-bogus version so build.Build itself
	// errors fast (no network, no makefile run); we just need to
	// know which validation branch caught it.
	var stderr bytes.Buffer
	rc := runBuild([]string{"99.99", "--force"}, nil, &stderr)
	_ = rc // the failure surface is in stderr text, not the code.
	if strings.Contains(stderr.String(), "only one version may be built") {
		t.Errorf("--force after positional regressed to positional; stderr=%q", stderr.String())
	}
}
