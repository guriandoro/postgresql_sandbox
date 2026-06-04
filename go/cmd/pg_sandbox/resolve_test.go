// Unit tests for resolveBinDir / resolveSandboxRoot. These pin the
// precedence ladder for the install root and sandbox root chains, so
// that future additions (a new layer, a new built-in default) have
// to land in this one file rather than coordinating four callers.
//
// The integration-shaped tests for the same chain — banner
// normalisation, relative path absolution, trailing-slash cleanup —
// live in argv_test.go (TestRunCleanupInstallVersions_*). Those
// exercise the helper through runCleanupInstallVersions's full
// pipeline; this file is the per-layer microscope.

package main

import (
	"path/filepath"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
)

// resetEnv clears every env var the helpers consult, then restores
// the test-supplied overrides. We can't use t.Setenv("", "") to
// clear a var that wasn't set (it would error on Unsetenv at
// cleanup); instead t.Setenv on each var with the empty string is
// equivalent for our purposes because both helpers treat empty as
// "unset" via os.Getenv == "".
func resetEnv(t *testing.T) {
	t.Helper()
	// HOME has to point somewhere for the "default ~/postgresql-
	// sandboxes" branch to work — point it at the test's tempdir so
	// the default resolves to a path under it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PGS_BIN_DIR", "")
	t.Setenv("PGS_SANDBOX_ROOT", "")
}

func TestResolveBinDir_flagWinsOverEnvGlobalAndDefault(t *testing.T) {
	resetEnv(t)
	t.Setenv("PGS_BIN_DIR", "/from/env")
	g := &config.Global{DefaultBinDir: "/from/global"}
	got, err := resolveBinDir("/from/flag", g)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if got != "/from/flag" {
		t.Errorf("got %q, want %q", got, "/from/flag")
	}
}

func TestResolveBinDir_envWinsOverGlobalAndDefault(t *testing.T) {
	resetEnv(t)
	t.Setenv("PGS_BIN_DIR", "/from/env")
	g := &config.Global{DefaultBinDir: "/from/global"}
	got, err := resolveBinDir("", g)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if got != "/from/env" {
		t.Errorf("got %q, want %q", got, "/from/env")
	}
}

func TestResolveBinDir_globalWinsOverDefault(t *testing.T) {
	resetEnv(t)
	g := &config.Global{DefaultBinDir: "/from/global"}
	got, err := resolveBinDir("", g)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if got != "/from/global" {
		t.Errorf("got %q, want %q", got, "/from/global")
	}
}

func TestResolveBinDir_defaultWhenAllEmpty(t *testing.T) {
	resetEnv(t)
	got, err := resolveBinDir("", nil)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if got != "/opt/postgresql" {
		t.Errorf("got %q, want %q", got, "/opt/postgresql")
	}
}

func TestResolveBinDir_nilGlobalIsSafe(t *testing.T) {
	// A nil *config.Global must not panic — it's the normal case
	// when no global config file exists (SPEC §3.3).
	resetEnv(t)
	t.Setenv("PGS_BIN_DIR", "/from/env")
	got, err := resolveBinDir("", nil)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if got != "/from/env" {
		t.Errorf("got %q, want %q", got, "/from/env")
	}
}

func TestResolveBinDir_resultIsAbsolute(t *testing.T) {
	// Pins the "filepath.Abs is applied unconditionally" contract.
	// A relative flag value must come back absolute — this is what
	// makes the banner / engine paths agree (see the 2026-06-04
	// pitfall and TestRunCleanupInstallVersions_relativeSandboxRoot).
	resetEnv(t)
	got, err := resolveBinDir("./relative", nil)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %q, want an absolute path", got)
	}
}

func TestResolveBinDir_trailingSlashIsCleaned(t *testing.T) {
	// filepath.Abs Cleans internally, so a trailing-slash absolute
	// input must come back de-trailed.
	resetEnv(t)
	got, err := resolveBinDir("/opt/postgresql/", nil)
	if err != nil {
		t.Fatalf("resolveBinDir: %v", err)
	}
	if got != "/opt/postgresql" {
		t.Errorf("got %q, want %q (trailing slash should be stripped)", got, "/opt/postgresql")
	}
}

func TestResolveSandboxRoot_flagWinsOverEnvGlobalAndDefault(t *testing.T) {
	resetEnv(t)
	t.Setenv("PGS_SANDBOX_ROOT", "/from/env")
	g := &config.Global{SandboxRoot: "/from/global"}
	got, err := resolveSandboxRoot("/from/flag", g)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	if got != "/from/flag" {
		t.Errorf("got %q, want %q", got, "/from/flag")
	}
}

func TestResolveSandboxRoot_envWinsOverGlobalAndDefault(t *testing.T) {
	resetEnv(t)
	t.Setenv("PGS_SANDBOX_ROOT", "/from/env")
	g := &config.Global{SandboxRoot: "/from/global"}
	got, err := resolveSandboxRoot("", g)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	if got != "/from/env" {
		t.Errorf("got %q, want %q", got, "/from/env")
	}
}

func TestResolveSandboxRoot_globalWinsOverDefault(t *testing.T) {
	resetEnv(t)
	g := &config.Global{SandboxRoot: "/from/global"}
	got, err := resolveSandboxRoot("", g)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	if got != "/from/global" {
		t.Errorf("got %q, want %q", got, "/from/global")
	}
}

func TestResolveSandboxRoot_defaultWhenAllEmpty(t *testing.T) {
	// resetEnv already pointed HOME at a fresh t.TempDir(), so the
	// default branch resolves to <tempdir>/postgresql-sandboxes.
	resetEnv(t)
	got, err := resolveSandboxRoot("", nil)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	// We don't pin the exact tempdir path (varies per run), only the
	// suffix and absoluteness — the contract is "default lives under
	// $HOME/postgresql-sandboxes".
	if !filepath.IsAbs(got) {
		t.Errorf("default %q is not absolute", got)
	}
	if filepath.Base(got) != "postgresql-sandboxes" {
		t.Errorf("default basename = %q, want %q", filepath.Base(got), "postgresql-sandboxes")
	}
}

func TestResolveSandboxRoot_missingHomeIsError(t *testing.T) {
	// When neither flag, env, nor global supplies a value, the
	// helper falls back to os.UserHomeDir(). If that fails (no $HOME
	// on Unix), the helper must return an error rather than silently
	// using an empty path — this is the only error path the helper
	// has, and callers translate it into ExitGeneric + a precise
	// "cannot determine home dir" message.
	t.Setenv("PGS_SANDBOX_ROOT", "")
	t.Setenv("HOME", "")
	_, err := resolveSandboxRoot("", nil)
	if err == nil {
		t.Errorf("want error when HOME is unset and no other layer supplies a value")
	}
}

func TestResolveSandboxRoot_resultIsAbsolute(t *testing.T) {
	resetEnv(t)
	got, err := resolveSandboxRoot("./relative", nil)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %q, want an absolute path", got)
	}
}

func TestResolveSandboxRoot_trailingSlashIsCleaned(t *testing.T) {
	resetEnv(t)
	got, err := resolveSandboxRoot("/tmp/sb/", nil)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	if got != "/tmp/sb" {
		t.Errorf("got %q, want %q (trailing slash should be stripped)", got, "/tmp/sb")
	}
}

func TestResolveSandboxRoot_nilGlobalIsSafe(t *testing.T) {
	resetEnv(t)
	t.Setenv("PGS_SANDBOX_ROOT", "/from/env")
	got, err := resolveSandboxRoot("", nil)
	if err != nil {
		t.Fatalf("resolveSandboxRoot: %v", err)
	}
	if got != "/from/env" {
		t.Errorf("got %q, want %q", got, "/from/env")
	}
}
