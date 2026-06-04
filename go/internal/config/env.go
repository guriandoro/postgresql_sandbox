// PGS_* environment variable overlay.
//
// SPEC §4.9 lists the environment variables this tool consumes.
// ApplyEnv takes the subset that maps to per-sandbox config fields
// (the rest are runtime-only: LOG_LEVEL, DEBUG, CONFIG_FILE,
// BUILD_DIR, etc.) and overlays them on top of a Sandbox value.
//
// Design choices:
//
//   - The lookup function is injected (`env func(string) string`)
//     rather than calling os.Getenv directly. Tests pass a closure
//     over a map; production passes os.Getenv. This is cheaper than
//     wrestling with os.Setenv in parallel tests.
//
//   - PGS_PORT failing to parse is an error, not a silent skip. A
//     typo in an env var is precisely the kind of "hidden state"
//     SPEC §3.1.7 forbids; refusing to start surfaces it loudly.
//
//   - Empty env values are treated as "unset". Otherwise users
//     would have to distinguish `PGS_HOST=` from "no PGS_HOST",
//     which they can't from a shell.

package config

import (
	"fmt"
	"strconv"
)

// ApplyEnv overlays per-sandbox PGS_* env vars onto s and returns
// the modified copy. Returns an error if a numeric env var failed
// to parse — we never silently substitute a bogus value.
//
// Per-sandbox vars handled: PGS_BIN_DIR, PGS_HOST, PGS_PORT,
// PGS_USER, PGS_DBNAME. Runtime-only vars (PGS_DEBUG,
// PGS_LOG_LEVEL, etc.) are ignored here — main.go reads those
// itself for things that aren't part of the sandbox's persisted
// state.
func ApplyEnv(s Sandbox, env func(string) string) (Sandbox, error) {
	if v := env("PGS_BIN_DIR"); v != "" {
		s.BinDir = v
	}
	if v := env("PGS_HOST"); v != "" {
		s.Host = v
	}
	if v := env("PGS_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return s, fmt.Errorf("config: PGS_PORT=%q: %w", v, err)
		}
		s.Port = p
	}
	if v := env("PGS_USER"); v != "" {
		s.Superuser = v
	}
	if v := env("PGS_DBNAME"); v != "" {
		s.DefaultDatabase = v
	}
	return s, nil
}

// ApplyEnvToGlobal overlays the host-wide PGS_* env vars onto g.
// Distinct function from ApplyEnv because the mapping differs:
// SandboxRoot, DefaultBinDir, and PgGatherDir are global-level
// concerns, not per-sandbox state.
func ApplyEnvToGlobal(g Global, env func(string) string) (Global, error) {
	if v := env("PGS_SANDBOX_ROOT"); v != "" {
		g.SandboxRoot = v
	}
	if v := env("PGS_BIN_DIR"); v != "" {
		// PGS_BIN_DIR fills both DefaultBinDir at the global
		// layer (so a user can set it once per shell) and
		// Sandbox.BinDir at the sandbox layer. We accept both
		// roles deliberately.
		g.DefaultBinDir = v
	}
	if v := env("PGS_PG_GATHER_DIR"); v != "" {
		g.PgGatherDir = v
	}
	return g, nil
}
