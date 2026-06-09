// Layered config resolution with source tracking.
//
// SPEC §3.1.2 defines the precedence order
//   1. flag (highest)
//   2. env
//   3. sandbox file
//   4. global file
//   5. built-in default (lowest)
// and SPEC §3.1.8 / §6.7 require that `config show` print, for each
// resolved value, the *layer* it came from. The Load* / ApplyEnv*
// helpers in this package merge layers but discard the source. This
// file adds the small "remember who wrote each field" mechanism the
// CLI needs and keeps it unit-testable in the package proper rather
// than in cmd/.
//
// Why a fresh file (rather than threading source through ApplyEnv,
// SaveSandbox, etc.)? Because the lifecycle commands don't care
// where a value came from once they've been told it — only `config
// show` does. Bolting provenance onto the existing read path would
// pay a cost on every code path for the benefit of one. So this file
// re-implements the merge in slow motion, with a Provenance entry
// per scalar field, used only by the `config` subcommand surface.
//
// Scope (deliberately narrow):
//
//   - Resolves only the *top-level scalar* Sandbox fields the user
//     can plausibly override across all five layers: Name, BinDir,
//     DataDir, LogFile, Host, Port, Superuser, DefaultDatabase, Role,
//     Cluster. The Physical/Logical sub-blocks are written by
//     deploy/promote/subscribe — never by env or flag — so they are
//     copied through verbatim from the sandbox-file layer without
//     per-field provenance. CreatedAt / LastModifiedAt are
//     system-managed and excluded entirely.
//
//   - ResolveGlobal does the same for Global with its smaller field
//     set.
//
//   - SettableKeys is consulted by `config set` to refuse mutation of
//     system-managed or load-bearing fields (see comments below for
//     the exact partitioning).

package config

import (
	"regexp"
	"strconv"
	"strings"
)

// Source identifies which layer of SPEC §3.1.2's precedence chain a
// given resolved value came from. Stored verbatim in --json output,
// so the string values are part of the user-visible contract.
type Source string

const (
	// SourceDefault: built-in Defaults() — bottom of the chain.
	SourceDefault Source = "default"
	// SourceGlobalFile: $XDG_CONFIG_HOME/pg_sandbox/config.json.
	SourceGlobalFile Source = "global-file"
	// SourceSandboxFile: <sandbox-dir>/pg_sandbox.json.
	SourceSandboxFile Source = "sandbox-file"
	// SourceEnv: a PGS_* environment variable.
	SourceEnv Source = "env"
	// SourceFlag: an explicit CLI flag at this invocation — top of
	// the chain.
	SourceFlag Source = "flag"
)

// Provenance records one resolved (key, value, source) triple. The
// Value field is `any` because the schema mixes ints (Port) and
// strings (everything else); callers format it for display.
type Provenance struct {
	// Key is the JSON field name as it appears on disk and as it
	// must be supplied to `config get` / `config set`.
	Key string
	// Value is the resolved value, in its native Go type. Strings
	// remain strings; ints remain ints; Role remains Role.
	Value any
	// Source is the layer that supplied the value.
	Source Source
}

// ResolveOptions bundles the inputs to Resolve. Every layer is
// optional — a nil pointer or empty map means "this layer didn't
// contribute anything", and Resolve falls through to the next layer.
//
// Env is a function (not a map) so callers can plug in os.Getenv
// directly without copying every PGS_* into a map; tests pass a
// closure over a map.
type ResolveOptions struct {
	// FlagsAsMap is keyed by JSON field name (e.g. "port", "host").
	// Only entries the user *explicitly* supplied on the command line
	// should be present; absent keys are treated as "not set" and
	// fall through. The values are the parsed Go types (int for
	// "port", string for the rest). Pass nil if no flags were given.
	FlagsAsMap map[string]any
	// Env is the env-lookup function. Typically os.Getenv in
	// production. Pass nil to skip the env layer entirely.
	Env func(string) string
	// Sandbox is the already-loaded per-sandbox config (or nil for
	// "no sandbox file present", e.g. during `config show --global`).
	Sandbox *Sandbox
	// Global is the already-loaded host-wide config (or nil if
	// missing — which is normal per SPEC §3.3).
	Global *Global
}

// Resolve walks the precedence chain for every top-level scalar
// Sandbox field and returns:
//
//   - merged: a Sandbox populated with the winning value at every
//     field (plus Physical/Logical copied through from Sandbox layer
//     verbatim — see file-level comment).
//
//   - prov: one Provenance entry per resolved field, in a stable
//     order matching ResolvedSandboxKeys. Stable order keeps `config
//     show` output diff-friendly across runs.
//
//   - err: only non-nil if a numeric layer (PGS_PORT, port flag)
//     contained garbage. Validation of the merged struct is left to
//     the caller via Validate().
func Resolve(opts ResolveOptions) (Sandbox, []Provenance, error) {
	merged := Defaults()
	prov := make([]Provenance, 0, len(ResolvedSandboxKeys))

	// Per-field resolution. Each call appends one Provenance entry.
	// The order here is the order in which `config show` will print
	// rows; keep it stable.
	var err error

	resolveStr := func(key string, defaultVal string, target *string,
		sandboxVal, globalVal string) {
		v, src := pickString(key, opts, sandboxVal, globalVal, defaultVal)
		*target = v
		prov = append(prov, Provenance{Key: key, Value: v, Source: src})
	}

	// schemaVersion comes from defaults or the sandbox file; it's
	// not user-overridable via env/flag. We still surface it in
	// `config show` so the user sees what version is on disk.
	{
		sv := merged.SchemaVersion
		src := SourceDefault
		if opts.Sandbox != nil && opts.Sandbox.SchemaVersion != 0 {
			sv = opts.Sandbox.SchemaVersion
			src = SourceSandboxFile
		}
		merged.SchemaVersion = sv
		prov = append(prov, Provenance{Key: "schemaVersion", Value: sv, Source: src})
	}

	// Strings with no env or flag mapping (only sandbox/global/default
	// can supply them). Name is the canonical example.
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.Name
		}
		v, src := pickString("name", opts, sandboxVal, "", "")
		merged.Name = v
		prov = append(prov, Provenance{Key: "name", Value: v, Source: src})
	}

	// BinDir: flag > env (PGS_BIN_DIR) > sandbox file > global
	// (defaultBinDir) > "" (no built-in default).
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.BinDir
		}
		globalVal := ""
		if opts.Global != nil {
			globalVal = opts.Global.DefaultBinDir
		}
		resolveStr("binDir", "", &merged.BinDir, sandboxVal, globalVal)
	}

	// dataDir / logFile: sandbox-file-only (no env or flag overrides
	// these — they're deploy-time decisions baked into the sandbox).
	// We still surface them in `config show` so the user can see the
	// values without grep'ing the JSON.
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.DataDir
		}
		v, src := pickString("dataDir", opts, sandboxVal, "", "")
		merged.DataDir = v
		prov = append(prov, Provenance{Key: "dataDir", Value: v, Source: src})
	}
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.LogFile
		}
		v, src := pickString("logFile", opts, sandboxVal, "", "")
		merged.LogFile = v
		prov = append(prov, Provenance{Key: "logFile", Value: v, Source: src})
	}

	// Host: flag > env (PGS_HOST) > sandbox file > default.
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.Host
		}
		resolveStr("host", "127.0.0.1", &merged.Host, sandboxVal, "")
	}

	// Port: the only int field. We hand-roll the resolution because
	// of the int-vs-string coercion at the env layer.
	{
		sandboxVal := 0
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.Port
		}
		// global doesn't expose port (DefaultPortBase is for the
		// port allocator, not a per-sandbox default).
		v, src, perr := pickPort("port", opts, sandboxVal, Defaults().Port)
		if perr != nil && err == nil {
			err = perr
		}
		merged.Port = v
		prov = append(prov, Provenance{Key: "port", Value: v, Source: src})
	}

	// Superuser / DefaultDatabase: flag > env > sandbox file >
	// default.
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.Superuser
		}
		resolveStr("superuser", "postgres", &merged.Superuser, sandboxVal, "")
	}
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.DefaultDatabase
		}
		resolveStr("defaultDatabase", "postgres", &merged.DefaultDatabase, sandboxVal, "")
	}

	// Role: sandbox-file or default. Not env/flag-overridable.
	{
		sandboxVal := Role("")
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.Role
		}
		role := RoleUnknown
		src := SourceDefault
		if sandboxVal != "" {
			role = sandboxVal
			src = SourceSandboxFile
		}
		merged.Role = role
		prov = append(prov, Provenance{Key: "role", Value: string(role), Source: src})
	}

	// Cluster: sandbox-file-only, optional.
	{
		sandboxVal := ""
		if opts.Sandbox != nil {
			sandboxVal = opts.Sandbox.Cluster
		}
		v, src := pickString("cluster", opts, sandboxVal, "", "")
		merged.Cluster = v
		prov = append(prov, Provenance{Key: "cluster", Value: v, Source: src})
	}

	// Pass through the sub-blocks and timestamps without provenance
	// — they're not part of the resolved-field surface.
	if opts.Sandbox != nil {
		merged.Physical = opts.Sandbox.Physical
		merged.Logical = opts.Sandbox.Logical
		merged.CreatedAt = opts.Sandbox.CreatedAt
		merged.LastModifiedAt = opts.Sandbox.LastModifiedAt
	}

	return merged, prov, err
}

// ResolvedSandboxKeys is the ordered list of JSON field names that
// Resolve returns provenance for. Exported so tests and the CLI can
// know the expected row count / order without re-running Resolve.
var ResolvedSandboxKeys = []string{
	"schemaVersion",
	"name",
	"binDir",
	"dataDir",
	"logFile",
	"host",
	"port",
	"superuser",
	"defaultDatabase",
	"role",
	"cluster",
}

// ResolvedGlobalKeys is the analog of ResolvedSandboxKeys for the
// global config.
var ResolvedGlobalKeys = []string{
	"schemaVersion",
	"sandboxRoot",
	"defaultBinDir",
	"pgGatherDir",
	"defaultPortBase",
	"defaultPortRange",
}

// ResolveGlobal resolves the host-wide Global config. Smaller layer
// set than per-sandbox: there is no "sandbox file" layer here (a
// global config IS the file), so the chain collapses to
// flag > env > global file > default.
//
// In practice `config show --global` is the only caller; it doesn't
// pass flags, so the chain effectively is env > file > default. The
// flag layer is wired anyway so the function shape mirrors Resolve.
func ResolveGlobal(opts ResolveOptions) (Global, []Provenance, error) {
	merged := GlobalDefaults()
	prov := make([]Provenance, 0, len(ResolvedGlobalKeys))

	// schemaVersion: file or default. Not user-overridable.
	{
		sv := merged.SchemaVersion
		src := SourceDefault
		if opts.Global != nil && opts.Global.SchemaVersion != 0 {
			sv = opts.Global.SchemaVersion
			src = SourceGlobalFile
		}
		merged.SchemaVersion = sv
		prov = append(prov, Provenance{Key: "schemaVersion", Value: sv, Source: src})
	}

	// sandboxRoot: env (PGS_SANDBOX_ROOT) > file > default ("").
	{
		fileVal := ""
		if opts.Global != nil {
			fileVal = opts.Global.SandboxRoot
		}
		v, src := pickGlobalString("sandboxRoot", opts, "PGS_SANDBOX_ROOT", fileVal, "")
		merged.SandboxRoot = v
		prov = append(prov, Provenance{Key: "sandboxRoot", Value: v, Source: src})
	}

	// defaultBinDir: env (PGS_BIN_DIR) > file > default ("").
	{
		fileVal := ""
		if opts.Global != nil {
			fileVal = opts.Global.DefaultBinDir
		}
		v, src := pickGlobalString("defaultBinDir", opts, "PGS_BIN_DIR", fileVal, "")
		merged.DefaultBinDir = v
		prov = append(prov, Provenance{Key: "defaultBinDir", Value: v, Source: src})
	}

	// pgGatherDir: env (PGS_PG_GATHER_DIR) > file > default ("").
	{
		fileVal := ""
		if opts.Global != nil {
			fileVal = opts.Global.PgGatherDir
		}
		v, src := pickGlobalString("pgGatherDir", opts, "PGS_PG_GATHER_DIR", fileVal, "")
		merged.PgGatherDir = v
		prov = append(prov, Provenance{Key: "pgGatherDir", Value: v, Source: src})
	}

	// defaultPortBase / defaultPortRange: file > default. No env.
	{
		v := merged.DefaultPortBase
		src := SourceDefault
		if opts.Global != nil && opts.Global.DefaultPortBase != 0 {
			v = opts.Global.DefaultPortBase
			src = SourceGlobalFile
		}
		merged.DefaultPortBase = v
		prov = append(prov, Provenance{Key: "defaultPortBase", Value: v, Source: src})
	}
	{
		v := merged.DefaultPortRange
		src := SourceDefault
		if opts.Global != nil && opts.Global.DefaultPortRange != 0 {
			v = opts.Global.DefaultPortRange
			src = SourceGlobalFile
		}
		merged.DefaultPortRange = v
		prov = append(prov, Provenance{Key: "defaultPortRange", Value: v, Source: src})
	}

	return merged, prov, nil
}

// SettableKeys returns the JSON field names that `config set` is
// allowed to mutate via KEY=VALUE. The partitioning rationale:
//
//   - settable: user-facing knobs that can be changed without
//     re-running deploy: host, port, superuser, defaultDatabase,
//     binDir (a user might point at a recompiled PG install of the
//     same major version), logFile (move the log).
//
//   - NOT settable (and `config set` returns ExitConfigKeyUnknown):
//     schemaVersion — managed by the binary
//     name          — would break sandbox lookup
//     dataDir       — would orphan a live PG data dir
//     role          — set by deploy/promote/subscribe
//     cluster       — set by cluster orchestration
//     physical/logical — replication block invariants
//     createdAt/lastModifiedAt — audit fields
//
// If we ever expand this list, also extend the switch in
// applySandboxSet (cmd/pg_sandbox/config.go).
func SettableKeys() []string {
	return []string{
		"host",
		"port",
		"superuser",
		"defaultDatabase",
		"binDir",
		"logFile",
	}
}

// IsSettableKey reports whether key is allowed to appear in a
// `config set KEY=VALUE` for a Sandbox config.
func IsSettableKey(key string) bool {
	for _, k := range SettableKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// pickString resolves a string-typed field across the
// flag > env > sandbox > global > default layers.
//
// The env-key mapping is field-specific (PGS_BIN_DIR for binDir,
// PGS_HOST for host, etc.); pickString handles that lookup itself
// rather than asking the caller to pre-compute it.
func pickString(key string, opts ResolveOptions, sandboxVal, globalVal, defaultVal string) (string, Source) {
	// 1. flag
	if v, ok := opts.FlagsAsMap[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s, SourceFlag
		}
	}
	// 2. env — only certain keys have an env mapping.
	if opts.Env != nil {
		if envKey := envKeyForSandbox(key); envKey != "" {
			if v := opts.Env(envKey); v != "" {
				return v, SourceEnv
			}
		}
	}
	// 3. sandbox file
	if sandboxVal != "" {
		return sandboxVal, SourceSandboxFile
	}
	// 4. global file
	if globalVal != "" {
		return globalVal, SourceGlobalFile
	}
	// 5. default
	return defaultVal, SourceDefault
}

// pickPort is pickString's specialization for the int Port field.
// Returns an error if the env or flag value can't be parsed as an
// int — silent fallback would hide a typo and SPEC §3.1.7 forbids
// that.
func pickPort(key string, opts ResolveOptions, sandboxVal, defaultVal int) (int, Source, error) {
	// 1. flag — accept either int (typical) or string (rare).
	if v, ok := opts.FlagsAsMap[key]; ok {
		switch x := v.(type) {
		case int:
			if x != 0 {
				return x, SourceFlag, nil
			}
		case string:
			if x != "" {
				p, err := strconv.Atoi(x)
				if err != nil {
					return 0, SourceFlag, err
				}
				return p, SourceFlag, nil
			}
		}
	}
	// 2. env (PGS_PORT)
	if opts.Env != nil {
		if v := opts.Env("PGS_PORT"); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil {
				return 0, SourceEnv, err
			}
			return p, SourceEnv, nil
		}
	}
	// 3. sandbox file
	if sandboxVal != 0 {
		return sandboxVal, SourceSandboxFile, nil
	}
	// 4. default (no global layer for port — global has
	// DefaultPortBase, which is the auto-allocator floor, not a
	// per-sandbox default port).
	return defaultVal, SourceDefault, nil
}

// pickGlobalString is the Global analog of pickString. The env-key
// mapping is passed explicitly because the Global field names don't
// 1:1 map to the PGS_* names.
func pickGlobalString(key string, opts ResolveOptions, envKey, fileVal, defaultVal string) (string, Source) {
	if v, ok := opts.FlagsAsMap[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s, SourceFlag
		}
	}
	if opts.Env != nil && envKey != "" {
		if v := opts.Env(envKey); v != "" {
			return v, SourceEnv
		}
	}
	if fileVal != "" {
		return fileVal, SourceGlobalFile
	}
	return defaultVal, SourceDefault
}

// envKeyForSandbox maps a JSON field name to its PGS_* env var, or
// "" if the field has no documented env override. Matches ApplyEnv's
// switch.
func envKeyForSandbox(key string) string {
	switch key {
	case "binDir":
		return "PGS_BIN_DIR"
	case "host":
		return "PGS_HOST"
	case "port":
		return "PGS_PORT"
	case "superuser":
		return "PGS_USER"
	case "defaultDatabase":
		return "PGS_DBNAME"
	}
	return ""
}

func NormalizeString(s string) string {
	re := regexp.MustCompile(`[A-Z]+`)

	return strings.ReplaceAll(re.ReplaceAllStringFunc(s, func(match string) string {
		return strings.ToLower(match)
	}), "-", "_")
}
