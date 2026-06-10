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
	"os"
	"path/filepath"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
)

// markSandbox drops the canonical marker file in dir so
// config.IsSandboxDir(dir) returns true. Returns the absolute path.
// Used by the resolveSandboxArg tests to fabricate "this is a
// sandbox" without bringing up a real PostgreSQL initdb run.
func markSandbox(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	marker := filepath.Join(dir, config.SandboxFilename)
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write %s: %v", marker, err)
	}
	return dir
}

// markCluster is the cluster-manifest sibling of markSandbox: drops
// pg_sandbox-cluster.json so config.IsClusterDir(dir) returns true
// without standing up real member sandboxes.
func markCluster(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	marker := filepath.Join(dir, config.ClusterFilename)
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write %s: %v", marker, err)
	}
	return dir
}

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

// resolveSandboxArg lets per-sandbox commands accept `-s name` from
// any working directory. The contract is local-first (existing
// invocations never change behavior) with a bare-name fallback into
// the configured sandboxRoot. Tests below pin each branch.

func TestResolveSandboxArg_emptyPassesThrough(t *testing.T) {
	// Empty input is the caller's "--sandbox-dir is required" path;
	// the helper must not rewrite it.
	resetEnv(t)
	if got := resolveSandboxArg("", nil); got != "" {
		t.Errorf("got %q, want %q", got, "")
	}
}

func TestResolveSandboxArg_localMatchWins(t *testing.T) {
	// A literal value that already points at a sandbox dir must be
	// returned untouched, regardless of what sandboxRoot says — this
	// is the "preserve historical cd-into-the-root then -s name"
	// workflow.
	resetEnv(t)
	tmp := t.TempDir()
	local := markSandbox(t, filepath.Join(tmp, "local"))
	rootElsewhere := t.TempDir()
	markSandbox(t, filepath.Join(rootElsewhere, "local")) // same name, but under sandboxRoot
	t.Setenv("PGS_SANDBOX_ROOT", rootElsewhere)
	got := resolveSandboxArg(local, nil)
	if got != local {
		t.Errorf("got %q, want %q (local match must win over sandboxRoot)", got, local)
	}
}

func TestResolveSandboxArg_pathWithSeparatorPassesThrough(t *testing.T) {
	// A value containing a path separator was an explicit path
	// statement by the user — even if it doesn't resolve to a
	// sandbox, we don't silently rewrite it to <root>/basename. That
	// would mask typos like `-s ./missign` → `<root>/missign`.
	resetEnv(t)
	tmp := t.TempDir()
	markSandbox(t, filepath.Join(tmp, "pg18"))
	t.Setenv("PGS_SANDBOX_ROOT", tmp)
	got := resolveSandboxArg("./missing/pg18", nil)
	if got != "./missing/pg18" {
		t.Errorf("got %q, want %q (paths with separators must not be rewritten)", got, "./missing/pg18")
	}
}

func TestResolveSandboxArg_bareNameResolvesUnderSandboxRoot(t *testing.T) {
	// The headline case: from any cwd, `-s pg18` must find
	// <sandboxRoot>/pg18.
	resetEnv(t)
	tmp := t.TempDir()
	want := markSandbox(t, filepath.Join(tmp, "pg18"))
	t.Setenv("PGS_SANDBOX_ROOT", tmp)
	got := resolveSandboxArg("pg18", nil)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveSandboxArg_bareNameNoMatchReturnsOriginal(t *testing.T) {
	// When the joined path is not a sandbox either, the helper
	// returns the user-typed token so the caller's "not a sandbox:
	// <name>" error reads naturally instead of leaking an opaque
	// joined path.
	resetEnv(t)
	t.Setenv("PGS_SANDBOX_ROOT", t.TempDir())
	got := resolveSandboxArg("nope", nil)
	if got != "nope" {
		t.Errorf("got %q, want %q", got, "nope")
	}
}

func TestResolveSandboxArg_usesGlobalConfigWhenEnvUnset(t *testing.T) {
	// Pins that the sandboxRoot lookup goes through the same layered
	// chain resolveSandboxRoot uses — including the global config —
	// not just PGS_SANDBOX_ROOT.
	resetEnv(t)
	tmp := t.TempDir()
	want := markSandbox(t, filepath.Join(tmp, "pg18"))
	g := &config.Global{SandboxRoot: tmp}
	got := resolveSandboxArg("pg18", g)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// resolveClusterArg mirrors resolveSandboxArg but gates on
// IsClusterDir. We pin the same four branches plus one cross-marker
// check (a sandbox-marked dir under sandboxRoot must NOT pose as a
// cluster, even though they share a parent root).

func TestResolveClusterArg_bareNameResolvesUnderSandboxRoot(t *testing.T) {
	resetEnv(t)
	tmp := t.TempDir()
	want := markCluster(t, filepath.Join(tmp, "mycluster"))
	t.Setenv("PGS_SANDBOX_ROOT", tmp)
	got := resolveClusterArg("mycluster", nil)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveClusterArg_bareNameNoMatchReturnsOriginal(t *testing.T) {
	resetEnv(t)
	t.Setenv("PGS_SANDBOX_ROOT", t.TempDir())
	got := resolveClusterArg("nope", nil)
	if got != "nope" {
		t.Errorf("got %q, want %q", got, "nope")
	}
}

func TestResolveClusterArg_pathWithSeparatorPassesThrough(t *testing.T) {
	resetEnv(t)
	tmp := t.TempDir()
	markCluster(t, filepath.Join(tmp, "mycluster"))
	t.Setenv("PGS_SANDBOX_ROOT", tmp)
	got := resolveClusterArg("./missing/mycluster", nil)
	if got != "./missing/mycluster" {
		t.Errorf("got %q, want %q", got, "./missing/mycluster")
	}
}

func TestResolveClusterArg_sandboxMarkerDoesNotPoseAsCluster(t *testing.T) {
	// Cross-marker isolation: a `-s pg18` at the cluster surface must
	// fall through to the "not a cluster" error path, NOT silently
	// succeed because pg_sandbox.json exists. The two markers gate
	// distinct surfaces and the SPEC treats them as non-substitutable.
	resetEnv(t)
	tmp := t.TempDir()
	markSandbox(t, filepath.Join(tmp, "pg18"))
	t.Setenv("PGS_SANDBOX_ROOT", tmp)
	got := resolveClusterArg("pg18", nil)
	if got != "pg18" {
		t.Errorf("got %q, want %q (sandbox marker must not satisfy cluster gate)", got, "pg18")
	}
}

// withDefaultInstallBase swaps the package-level defaultInstallBase for
// the duration of a test (restored via t.Cleanup), so install
// discovery can be pointed at a fabricated temp tree instead of the
// real /opt/postgresql. NOT parallel-safe — callers must not t.Parallel.
func withDefaultInstallBase(t *testing.T, base string) {
	t.Helper()
	prev := defaultInstallBase
	defaultInstallBase = base
	t.Cleanup(func() { defaultInstallBase = prev })
}

// fakeInstall fabricates <base>/<version>/bin/psql as an executable
// stub so latestInstalledBinDir treats <version> as a usable install.
func fakeInstall(t *testing.T, base, version string) {
	t.Helper()
	binDir := filepath.Join(base, version, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	psql := filepath.Join(binDir, "psql")
	if err := os.WriteFile(psql, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", psql, err)
	}
}

func TestLatestInstalledBinDir_picksNumericLatest(t *testing.T) {
	base := t.TempDir()
	// 17.10 must beat 17.9 (numeric, not lexicographic) and 16.5.
	fakeInstall(t, base, "16.5")
	fakeInstall(t, base, "17.9")
	fakeInstall(t, base, "17.10")
	path, version, ok := latestInstalledBinDir(base)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if version != "17.10" {
		t.Errorf("version = %q, want 17.10", version)
	}
	if want := filepath.Join(base, "17.10"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestLatestInstalledBinDir_skipsNonVersionAndPartial(t *testing.T) {
	base := t.TempDir()
	fakeInstall(t, base, "16.5")
	// A non-numeric dir (e.g. a build tree) must be ignored.
	if err := os.MkdirAll(filepath.Join(base, "src", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(base, "src", "bin", "psql"), []byte("x"), 0o755)
	// A version-shaped dir WITHOUT an executable psql must be skipped,
	// even though "18.0" > "16.5" numerically.
	if err := os.MkdirAll(filepath.Join(base, "18.0", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, version, ok := latestInstalledBinDir(base)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if version != "16.5" {
		t.Errorf("version = %q, want 16.5 (18.0 has no psql, src is non-version)", version)
	}
}

func TestLatestInstalledBinDir_missingBaseIsNotOK(t *testing.T) {
	if _, _, ok := latestInstalledBinDir(filepath.Join(t.TempDir(), "nope")); ok {
		t.Error("ok = true for a missing base, want false")
	}
}

func TestLatestInstalledBinDir_emptyBaseIsNotOK(t *testing.T) {
	if _, _, ok := latestInstalledBinDir(t.TempDir()); ok {
		t.Error("ok = true for an empty base, want false")
	}
}

func TestVersionKeyLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"17.9", "17.10", true},  // numeric, not lexicographic
		{"17.10", "17.9", false},
		{"16.5", "17.1", true},
		{"17", "17.1", true},     // shorter treated as .0
		{"17.0", "17", false},
		{"18.3", "18.3", false},
	}
	for _, c := range cases {
		ka, oka := parseVersionKey(c.a)
		kb, okb := parseVersionKey(c.b)
		if !oka || !okb {
			t.Fatalf("parseVersionKey failed for %q/%q", c.a, c.b)
		}
		if got := versionKeyLess(ka, kb); got != c.want {
			t.Errorf("versionKeyLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestParseVersionKey_rejectsNonNumeric(t *testing.T) {
	for _, name := range []string{"src", "18.x", "", "17-rc1", "v17"} {
		if _, ok := parseVersionKey(name); ok {
			t.Errorf("parseVersionKey(%q) ok = true, want false", name)
		}
	}
}
