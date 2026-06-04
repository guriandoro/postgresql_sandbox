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

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
)

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
