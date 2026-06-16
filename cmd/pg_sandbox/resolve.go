// Shared CLI-layer resolution for the install root (binDir) and the
// sandbox root (sandboxRoot). Before this file existed, four
// dispatchers — cleanup_install_versions.go, build.go, report.go,
// global_status.go — each open-coded the same flag → env → global
// config → built-in default ladder. Keeping the chain duplicated was
// the structural reason the 2026-06-04 banner / scope mismatch class
// of bugs kept being possible: adding a new layer (e.g. project-local
// config) or changing the built-in default (e.g. XDG-flavored
// ~/.local/share/pg_sandbox) required four files to move in
// lockstep, and any caller that drifted printed one path while the
// engine walked another.
//
// The two helpers below capture only what was actually duplicated.
// They deliberately do NOT subsume callers whose chain has a
// real semantic difference, namely:
//
//   - report.go's binDir resolution has NO built-in default — it
//     returns ExitPgGatherDirMissing / ExitUsage when nothing
//     supplies the value, because rendering a report without an
//     install root is meaningless. That caller still loads the
//     global config and walks the flag/env/global layers inline.
//
// Both helpers follow the design sketch from the code-review fix
// brief: lowercase (unexported) — dispatcher-internal API, not a
// public contract — and the resolved path is always run through
// filepath.Abs so the caller can print it in banners and pass it to
// engines (which Clean their inputs internally) without textual
// drift between the two.
//
// internal/config also has pickGlobalString / pickString, but those
// serve `config show`'s provenance path (they return a Source label
// alongside the value) and have a different shape for a different
// purpose. We don't share code with them — wrapping wouldn't save
// lines and would couple two surfaces that should be free to evolve
// independently.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/fsutil"
)

// defaultInstallBase is the conventional parent dir holding one subdir
// per installed PostgreSQL version (16.5, 17.4, 18.3, ...). It mirrors
// the built-in default resolveBinDir falls back to, but is kept as a
// package var (not a const) so tests can point discovery at a temp dir
// instead of the real /opt/postgresql.
var defaultInstallBase = "/opt/postgresql"

// latestInstalledBinDir scans base for version-shaped subdirs that
// contain a runnable bin/psql and returns the path + version of the
// numerically-highest one. ok is false when base is missing/unreadable
// or holds no usable install. This is discovery only — it never builds
// anything.
//
// Used by `report`, which (unlike deploy/build) takes no <version>
// argument: when no --bin-dir / PGS_BIN_DIR / global defaultBinDir is
// supplied we pick the newest install under base rather than erroring,
// so a bare `pg_sandbox report --input out.txt` works out of the box.
//
// "Usable" means bin/psql is executable: the report only ever runs
// initdb / pg_ctl / psql, all in the same bin/, so psql's presence is
// a sufficient marker and skips partial or broken installs. Version
// comparison is numeric and element-wise (NOT the lexicographic sort
// cleanup uses) so 17.10 correctly outranks 17.9.
func latestInstalledBinDir(base string) (path, version string, ok bool) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", "", false
	}
	var bestName string
	var bestKey []int
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		key, parsed := parseVersionKey(e.Name())
		if !parsed {
			continue
		}
		if !isExecutableFile(filepath.Join(base, e.Name(), "bin", "psql")) {
			continue
		}
		if bestName == "" || versionKeyLess(bestKey, key) {
			bestName, bestKey = e.Name(), key
		}
	}
	if bestName == "" {
		return "", "", false
	}
	return filepath.Join(base, bestName), bestName, true
}

// parseVersionKey splits a dir name like "17.4" (or "17", "9.6.24")
// into its numeric components. Returns ok=false for anything that
// isn't purely dot-separated non-negative integers, which excludes
// stray dirs like "src" or "build" from version discovery.
func parseVersionKey(name string) (key []int, ok bool) {
	parts := strings.Split(name, ".")
	key = make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		key[i] = n
	}
	return key, true
}

// versionKeyLess reports whether a sorts before b, comparing
// element-wise and treating a missing component as 0 (so "17" < "17.1"
// and "17.9" < "17.10").
func versionKeyLess(a, b []int) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

// isExecutableFile reports whether path is a regular, runnable file.
// Mirrors pgexec.isExecutable (kept local to avoid widening that
// package's API for a CLI-layer concern).
func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0o111 != 0
}

// resolveBinDir picks the install root from the layered chain:
//
//  1. explicit flag value (non-empty)
//  2. PGS_BIN_DIR env var
//  3. global config's defaultBinDir (when globalCfg != nil)
//  4. built-in default "/opt/postgresql"
//
// The result is filepath.Abs'd so callers can show it in banners and
// rely on it being absolute. If Abs fails (e.g. Getwd error), the
// path is returned as-is — the existing inline blocks all used the
// same "swallow + leave as-is" precedent and changing it would alter
// behavior under failure.
func resolveBinDir(flagValue string, globalCfg *config.Global) (string, error) {
	v := flagValue
	if v == "" {
		v = os.Getenv("PGS_BIN_DIR")
	}
	if v == "" && globalCfg != nil {
		v = globalCfg.DefaultBinDir
	}
	if v == "" {
		v = "/opt/postgresql"
	}
	v = fsutil.ExpandTilde(v)
	if abs, err := filepath.Abs(v); err == nil {
		v = abs
	}
	return v, nil
}

// resolveSandboxRoot picks the sandbox root from the layered chain:
//
//  1. explicit flag value (non-empty)
//  2. PGS_SANDBOX_ROOT env var
//  3. global config's sandboxRoot (when globalCfg != nil)
//  4. built-in default ~/postgresql-sandboxes/
//
// Unlike resolveBinDir, the built-in default requires reading
// os.UserHomeDir(), which can fail (no $HOME set, broken /etc/passwd
// lookup). That's the only error path: an explicit flag, env value,
// or global config value short-circuits before we ever consult
// UserHomeDir. The error is returned so callers can emit a precise
// "cannot determine home dir" message and exit with ExitGeneric.
//
// The result is filepath.Abs'd, matching resolveBinDir.
func resolveSandboxRoot(flagValue string, globalCfg *config.Global) (string, error) {
	v := flagValue
	if v == "" {
		v = os.Getenv("PGS_SANDBOX_ROOT")
	}
	if v == "" && globalCfg != nil {
		v = globalCfg.SandboxRoot
	}
	if v == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home dir: %w", err)
		}
		v = filepath.Join(home, "postgresql-sandboxes")
	}
	v = fsutil.ExpandTilde(v)
	if abs, err := filepath.Abs(v); err == nil {
		v = abs
	}
	return v, nil
}

// loadGlobalConfig is the canonical "best-effort, silent on missing"
// loader used by every dispatcher that calls the resolveBinDir /
// resolveSandboxRoot helpers. Returns nil when the global config
// path can't be determined OR when the file can't be loaded — both
// are normal per SPEC §3.3 (the global file is optional). Callers
// always pass the result through to the resolve helpers, which
// handle nil gracefully.
func loadGlobalConfig() *config.Global {
	gp, err := config.GlobalConfigPath()
	if err != nil {
		return nil
	}
	g, err := config.LoadGlobal(gp)
	if err != nil {
		return nil
	}
	return g
}

// resolveSandboxArg lets per-sandbox commands accept a bare sandbox
// name in addition to a path. The original surface (`-s ./pg18`,
// `-s /abs/path/pg18`) is preserved; the new surface (`-s pg18` from
// any working directory) is additive.
//
// Resolution order — strictly local-first, so no existing invocation
// changes behavior:
//
//  1. Empty → return "" so the caller's "--sandbox-dir is required"
//     check fires unchanged.
//  2. The literal value already resolves to a sandbox dir → return
//     it untouched. This covers `-s .`, `-s ./pg18`, `-s /abs/path`,
//     and the historical "cd into the sandbox-root, then -s name"
//     workflow.
//  3. The value contains a path separator → return untouched. The
//     user wrote a path; let the existing IsSandboxDir + error path
//     speak for themselves (this avoids `-s ./missing` silently
//     resolving to a same-named sandbox under sandboxRoot).
//  4. Bare name → join onto the resolved sandboxRoot and return THAT
//     if it's a sandbox dir; otherwise return the original (so the
//     caller's existing "not a sandbox: <name>" error fires with
//     the user-typed token, not the joined path).
//
// Best-effort: any failure to determine sandboxRoot (e.g. HOME unset
// AND no flag/env/global value) is swallowed by returning the input
// untouched. The point of this helper is convenience, not a new
// failure surface.
func resolveSandboxArg(raw string, globalCfg *config.Global) string {
	if raw == "" {
		return raw
	}
	if config.IsSandboxDir(raw) {
		return raw
	}
	if strings.ContainsRune(raw, filepath.Separator) {
		return raw
	}
	root, err := resolveSandboxRoot("", globalCfg)
	if err != nil {
		return raw
	}
	candidate := filepath.Join(root, raw)
	if config.IsSandboxDir(candidate) {
		return candidate
	}
	return raw
}

// resolveClusterArg is the cluster sibling of resolveSandboxArg —
// same local-first / has-separator-passes-through / bare-name-under-
// sandboxRoot rules, gated on config.IsClusterDir instead of
// IsSandboxDir. Used by `cluster status` and `cluster destroy` so
// `pg_sandbox cluster status -s mycluster` works from any cwd.
//
// We keep this as a separate helper rather than parametrising
// resolveSandboxArg on a predicate: the two markers live in
// different schema files (pg_sandbox.json vs pg_sandbox-cluster.json)
// and the SPEC treats sandboxes and clusters as distinct surfaces,
// so a typed helper per surface reads better at the call sites than
// a generic one would.
func resolveClusterArg(raw string, globalCfg *config.Global) string {
	if raw == "" {
		return raw
	}
	if config.IsClusterDir(raw) {
		return raw
	}
	if strings.ContainsRune(raw, filepath.Separator) {
		return raw
	}
	root, err := resolveSandboxRoot("", globalCfg)
	if err != nil {
		return raw
	}
	candidate := filepath.Join(root, raw)
	if config.IsClusterDir(candidate) {
		return candidate
	}
	return raw
}
