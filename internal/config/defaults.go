// Built-in defaults.
//
// These are the bottom of the resolution chain (SPEC §3.1.5): when
// no flag, env, sandbox file, or global file specifies a value, the
// resolver falls back to whatever Defaults returns.
//
// Three fields have NO built-in default — Name, BinDir, DataDir,
// and LogFile. They depend on the sandbox's location and the user's
// PostgreSQL install, so the caller MUST supply them before Save
// (Validate enforces this).

package config

import "github.com/guriandoro/postgresql_sandbox/internal/portalloc"

// Defaults returns a Sandbox populated with the built-in default
// values for fields that have one. Fields without a default are
// left zero; the caller fills them before validation.
func Defaults() Sandbox {
	return Sandbox{
		SchemaVersion:   CurrentSchemaVersion,
		Host:            "127.0.0.1",
		Port:            portalloc.DefaultBasePort,
		Superuser:       "postgres",
		DefaultDatabase: "postgres",
		Role:            RoleUnknown,
	}
}

// GlobalDefaults returns a Global populated with the built-in
// defaults the tool uses when no global config file exists. Mostly
// useful for documentation / `config show --global` output.
func GlobalDefaults() Global {
	return Global{
		SchemaVersion:    CurrentSchemaVersion,
		DefaultPortBase:  portalloc.DefaultBasePort,
		DefaultPortRange: portalloc.DefaultRange,
	}
}
