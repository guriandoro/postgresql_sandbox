// CLI wiring for `pg_sandbox config`. SPEC §6.7.
//
// `config` is a meta-command with five sub-subcommands:
//
//   show     — print resolved config with per-field source layer
//   get      — print one resolved value to stdout
//   set      — atomic, validated multi-key mutation on disk
//   validate — schema-check the on-disk file without modifying it
//   migrate  — convert a Python-era pg_sandbox.env to the new format
//
// Design notes worth calling out:
//
//   - The dispatcher in runConfig deliberately mirrors the top-level
//     main.go dispatcher: read args[0], look it up in a small map,
//     hand off args[1:]. Same shape, so contributors learn the
//     pattern once.
//
//   - Each sub-subcommand owns its FlagSet and exit-code path. They
//     are self-contained for testability and to keep the file
//     scannable.
//
//   - Per SPEC §4.6, all diagnostics go to stderr and only
//     machine-consumable output (the resolved value for `get`, the
//     JSON for `show --json`, the human table for `show`) goes to
//     stdout.
//
//   - `set` is *atomic at the API layer*: every KEY=VALUE is parsed
//     before any mutation, every value is coerced and per-field-
//     validated, the merged struct is full-validated, and only then
//     does SaveSandbox (or SaveGlobal) write to disk. A typo'd value
//     never leaves a half-mutated file.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runConfig is the dispatcher for `config`. Pattern matches the
// top-level dispatcher in main.go: args[0] is the sub-subcommand
// name, args[1:] is passed through. As with the top-level dispatcher,
// any --debug / --quiet / --color that landed at the head of args
// (because they appeared before the sub-subcommand name) is captured
// and re-prepended onto args[1:] so the leaf FlagSet sees them in
// the position it expects.
func runConfig(args []string, stdout, stderr io.Writer) int {
	leading, args := captureGlobalFlags(args)
	if len(args) == 0 {
		printConfigUsage(stderr)
		return ui.ExitUsage.Int()
	}
	sub := args[0]
	rest := args[1:]
	if len(leading) > 0 {
		rest = append(append([]string{}, leading...), rest...)
	}
	switch sub {
	case "show":
		return runConfigShow(rest, stdout, stderr)
	case "get":
		return runConfigGet(rest, stdout, stderr)
	case "set":
		return runConfigSet(rest, stdout, stderr)
	case "validate":
		return runConfigValidate(rest, stdout, stderr)
	case "migrate":
		return runConfigMigrate(rest, stdout, stderr)
	case "--help", "-h", "help":
		printConfigUsage(stdout)
		return ui.ExitOK.Int()
	default:
		fmt.Fprintf(stderr, "pg_sandbox config: unknown subcommand %q\n", sub)
		printConfigUsage(stderr)
		return ui.ExitUsage.Int()
	}
}

// printConfigUsage writes the `config` help text. Used both for the
// no-args / unknown-subcommand error paths (to stderr) and for
// `config --help` (to stdout).
func printConfigUsage(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox config — inspect/mutate sandbox or global config (SPEC §6.7)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox config show     (-s <dir>|--global) [--json]")
	fmt.Fprintln(w, "  pg_sandbox config get      (-s <dir>|--global) <key>")
	fmt.Fprintln(w, "  pg_sandbox config set      (-s <dir>|--global) <key>=<value> [<key>=<value> ...]")
	fmt.Fprintln(w, "  pg_sandbox config validate (-s <dir>|--global)")
	fmt.Fprintln(w, "  pg_sandbox config migrate  -s <dir> [--replace]")
}

// scope captures the (--sandbox-dir | --global) selection in one
// place. Exactly one MUST be set. Returned by parseScopeFlags so the
// per-subcommand functions don't each re-implement the validation.
type scope struct {
	sandboxDir string // populated when --sandbox-dir / -s was used
	global     bool   // true when --global was used
}

// parseScopeFlags wires the -s/--sandbox-dir and --global flags onto
// fs (so the flag package emits its own --help with them) and
// returns the parsed scope after fs.Parse has run. The required-
// exactly-one check is the caller's job; this helper just collects.
func parseScopeFlags(fs *flag.FlagSet, sc *scope) {
	fs.StringVar(&sc.sandboxDir, "sandbox-dir", "", "Target sandbox directory")
	fs.StringVar(&sc.sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&sc.global, "global", false, "Operate on the host-wide global config")
}

// validateScope enforces exactly-one. Returns a nicely-formatted
// error suitable for direct stderr printing.
func validateScope(name string, sc scope) error {
	switch {
	case sc.sandboxDir == "" && !sc.global:
		return fmt.Errorf("pg_sandbox config %s: one of --sandbox-dir/-s or --global is required", name)
	case sc.sandboxDir != "" && sc.global:
		return fmt.Errorf("pg_sandbox config %s: --sandbox-dir and --global are mutually exclusive", name)
	}
	return nil
}

// ---------------------------------------------------------------- //
// config show
// ---------------------------------------------------------------- //

// runConfigShow implements `pg_sandbox config show`. SPEC §6.7.
//
// Output rules:
//   - default human: aligned three-column key/value/source table to
//     stdout, one row per resolved field.
//   - --json: a JSON object {config: <merged>, sources: <map>} to
//     stdout, pretty-printed, newline-terminated.
func runConfigShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var sc scope
	var asJSON bool
	parseScopeFlags(fs, &sc)
	fs.BoolVar(&asJSON, "json", false, "Emit machine-readable JSON to stdout")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if err := validateScope("show", sc); err != nil {
		fmt.Fprintln(stderr, err)
		return ui.ExitUsage.Int()
	}

	if sc.global {
		return showGlobal(asJSON, stdout, stderr)
	}
	return showSandbox(sc.sandboxDir, asJSON, stdout, stderr)
}

// showSandbox renders the per-sandbox config view.
func showSandbox(sandboxDir string, asJSON bool, stdout, stderr io.Writer) int {
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox config show: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	sb, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: load: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	// Global is OPTIONAL (SPEC §3.3). LoadGlobal returns (nil, nil)
	// when the file is absent.
	g := loadGlobalBestEffort(stderr)

	merged, prov, err := config.Resolve(config.ResolveOptions{
		Env:     os.Getenv,
		Sandbox: sb,
		Global:  g,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: resolve: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	if asJSON {
		return emitShowJSON(stdout, stderr, merged, prov)
	}
	emitShowTable(stdout, prov)
	return ui.ExitOK.Int()
}

// showGlobal renders the host-wide config view.
func showGlobal(asJSON bool, stdout, stderr io.Writer) int {
	path, err := config.GlobalConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: global path: %v\n", err)
		return ui.ExitGeneric.Int()
	}
	g, err := config.LoadGlobal(path)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: load %s: %v\n", path, err)
		return ui.ExitBadConfig.Int()
	}
	merged, prov, err := config.ResolveGlobal(config.ResolveOptions{
		Env:    os.Getenv,
		Global: g,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: resolve: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	if asJSON {
		return emitShowJSON(stdout, stderr, merged, prov)
	}
	emitShowTable(stdout, prov)
	return ui.ExitOK.Int()
}

// loadGlobalBestEffort reads the global config if it exists; a
// missing or unreadable file is silently treated as "no global
// layer". Only a syntactic error in an existing file would surface,
// but we already accept SPEC §3.3 "global is optional" — so we
// report the error to stderr but still proceed with nil so `show`
// can render the rest.
func loadGlobalBestEffort(stderr io.Writer) *config.Global {
	path, err := config.GlobalConfigPath()
	if err != nil {
		return nil
	}
	g, err := config.LoadGlobal(path)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: warning: global config %s: %v\n", path, err)
		return nil
	}
	return g
}

// emitShowJSON serializes the merged struct + per-key sources map.
// `merged` is typed `any` because show is called with both Sandbox
// and Global; encoding/json handles both shapes the same way.
func emitShowJSON(stdout, stderr io.Writer, merged any, prov []config.Provenance) int {
	sources := make(map[string]config.Source, len(prov))
	for _, p := range prov {
		sources[p.Key] = p.Source
	}
	payload := struct {
		Config  any                      `json:"config"`
		Sources map[string]config.Source `json:"sources"`
	}{Config: merged, Sources: sources}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config show: marshal: %v\n", err)
		return ui.ExitGeneric.Int()
	}
	fmt.Fprintln(stdout, string(data))
	return ui.ExitOK.Int()
}

// emitShowTable writes a three-column "key / value / source" table.
// We align the columns by walking once to compute widths, then
// printing. Stdlib only; no text/tabwriter to keep the format
// predictable (tabwriter inserts column-specific whitespace that
// changes with terminal width).
func emitShowTable(stdout io.Writer, prov []config.Provenance) {
	const (
		hKey    = "KEY"
		hValue  = "VALUE"
		hSource = "SOURCE"
	)
	maxKey, maxVal := len(hKey), len(hValue)
	for _, p := range prov {
		if l := len(p.Key); l > maxKey {
			maxKey = l
		}
		if l := len(formatValue(p.Value)); l > maxVal {
			maxVal = l
		}
	}
	// 2-space gutter between columns.
	fmt.Fprintf(stdout, "%-*s  %-*s  %s\n", maxKey, hKey, maxVal, hValue, hSource)
	for _, p := range prov {
		fmt.Fprintf(stdout, "%-*s  %-*s  %s\n",
			maxKey, p.Key,
			maxVal, formatValue(p.Value),
			string(p.Source))
	}
}

// formatValue is the human-display formatting for a Provenance
// Value. Strings come through verbatim (including ""), ints via
// strconv.Itoa, everything else via fmt.Sprintf("%v").
func formatValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ---------------------------------------------------------------- //
// config get
// ---------------------------------------------------------------- //

// runConfigGet implements `pg_sandbox config get`. SPEC §6.7.
//
// Behavior: print the *resolved* value (i.e. after env / file /
// default merge) for the requested key to stdout. Exit
// ExitConfigKeyUnknown when the key isn't in the schema.
func runConfigGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var sc scope
	parseScopeFlags(fs, &sc)
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if err := validateScope("get", sc); err != nil {
		fmt.Fprintln(stderr, err)
		return ui.ExitUsage.Int()
	}
	tail := fs.Args()
	if len(tail) != 1 {
		fmt.Fprintln(stderr, "pg_sandbox config get: exactly one KEY argument is required")
		return ui.ExitUsage.Int()
	}
	key := tail[0]

	var prov []config.Provenance
	if sc.global {
		path, err := config.GlobalConfigPath()
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config get: global path: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		g, err := config.LoadGlobal(path)
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config get: load %s: %v\n", path, err)
			return ui.ExitBadConfig.Int()
		}
		_, prov, err = config.ResolveGlobal(config.ResolveOptions{Env: os.Getenv, Global: g})
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config get: resolve: %v\n", err)
			return ui.ExitBadConfig.Int()
		}
	} else {
		sc.sandboxDir = resolveSandboxArg(sc.sandboxDir, loadGlobalConfig())
		if !config.IsSandboxDir(sc.sandboxDir) {
			fmt.Fprintf(stderr, "pg_sandbox config get: not a sandbox: %s\n", sc.sandboxDir)
			return ui.ExitNotASandbox.Int()
		}
		sb, err := config.LoadSandbox(sc.sandboxDir)
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config get: load: %v\n", err)
			return ui.ExitBadConfig.Int()
		}
		g := loadGlobalBestEffort(stderr)
		_, prov, err = config.Resolve(config.ResolveOptions{
			Env: os.Getenv, Sandbox: sb, Global: g,
		})
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config get: resolve: %v\n", err)
			return ui.ExitBadConfig.Int()
		}
	}

	for _, p := range prov {
		if p.Key == key {
			fmt.Fprintln(stdout, formatValue(p.Value))
			return ui.ExitOK.Int()
		}
	}
	fmt.Fprintf(stderr, "pg_sandbox config get: unknown key %q\n", key)
	return ui.ExitConfigKeyUnknown.Int()
}

// ---------------------------------------------------------------- //
// config set
// ---------------------------------------------------------------- //

// runConfigSet implements `pg_sandbox config set`. SPEC §6.7.
//
// Behavior:
//  1. Load the current config (or start from Defaults if --global
//     and no file exists yet).
//  2. Parse every KEY=VALUE pair FIRST. A malformed pair fails the
//     whole call before any mutation.
//  3. Coerce + per-field-validate each value.
//  4. Apply to the in-memory struct.
//  5. Full-validate the merged struct.
//  6. Save atomically.
//  7. Log each applied key at info level to stderr.
func runConfigSet(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var sc scope
	parseScopeFlags(fs, &sc)
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if err := validateScope("set", sc); err != nil {
		fmt.Fprintln(stderr, err)
		return ui.ExitUsage.Int()
	}
	pairs := fs.Args()
	if len(pairs) == 0 {
		fmt.Fprintln(stderr, "pg_sandbox config set: at least one KEY=VALUE pair is required")
		return ui.ExitUsage.Int()
	}

	// Step 2: parse all pairs before any mutation. Sort keys for a
	// deterministic order in error messages and in the log lines we
	// emit on success.
	parsed := make([]kvPair, 0, len(pairs))
	for _, raw := range pairs {
		k, v, err := splitKV(raw)
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config set: %v\n", err)
			return ui.ExitUsage.Int()
		}
		parsed = append(parsed, kvPair{Key: k, Raw: v})
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Key < parsed[j].Key })

	if sc.global {
		return applyGlobalSet(parsed, stderr)
	}
	return applySandboxSet(sc.sandboxDir, parsed, stderr)
}

// kvPair is the parsed shape of one KEY=VALUE token.
type kvPair struct {
	Key string
	Raw string
}

// splitKV parses "KEY=VALUE" into (key, value). The value may
// contain additional '=' (we split on the first one only) so users
// can `config set sandboxRoot=/path=with=equals` if they ever need
// to.
func splitKV(s string) (string, string, error) {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", fmt.Errorf("malformed pair %q (expected KEY=VALUE)", s)
	}
	key := s[:eq]
	val := s[eq+1:]
	return key, val, nil
}

// applySandboxSet handles the per-sandbox branch of `config set`.
func applySandboxSet(sandboxDir string, parsed []kvPair, stderr io.Writer) int {
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox config set: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	sb, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config set: load: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	// Step 3 & 4: coerce + apply. We mutate a *copy* until we know
	// every key parsed successfully, then commit. The intermediate
	// state never reaches disk.
	staged := *sb
	for _, p := range parsed {
		if !config.IsSettableKey(p.Key) {
			fmt.Fprintf(stderr, "pg_sandbox config set: key %q is not settable (run `pg_sandbox config show` to see the schema)\n", p.Key)
			return ui.ExitConfigKeyUnknown.Int()
		}
		if err := applySandboxKey(&staged, p.Key, p.Raw); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config set: %v\n", err)
			return ui.ExitBadConfig.Int()
		}
	}

	// Step 5: full struct validation. Catches inter-field
	// consistency (e.g. role=standby but no physical block).
	if err := config.Validate(&staged); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config set: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	// Step 6: atomic save.
	if err := config.SaveSandbox(sandboxDir, &staged); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config set: save: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	// Step 7: one log line per applied key.
	logSetApplied(stderr, parsed)
	return ui.ExitOK.Int()
}

// applyGlobalSet handles the --global branch of `config set`. If the
// global file does not yet exist we synthesize one from
// GlobalDefaults() — this is the documented "first set creates the
// file" behavior implied by SPEC §3.3 ("OPTIONAL").
func applyGlobalSet(parsed []kvPair, stderr io.Writer) int {
	path, err := config.GlobalConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config set: global path: %v\n", err)
		return ui.ExitGeneric.Int()
	}
	existing, err := config.LoadGlobal(path)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config set: load %s: %v\n", path, err)
		return ui.ExitBadConfig.Int()
	}
	staged := config.GlobalDefaults()
	if existing != nil {
		staged = *existing
		if staged.SchemaVersion == 0 {
			staged.SchemaVersion = config.CurrentSchemaVersion
		}
	}
	for _, p := range parsed {
		if err := applyGlobalKey(&staged, p.Key, p.Raw); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config set: %v\n", err)
			// Unknown key → ExitConfigKeyUnknown; malformed value
			// → ExitBadConfig. applyGlobalKey signals the
			// distinction via a sentinel error.
			if errors.Is(err, errUnknownKey) {
				return ui.ExitConfigKeyUnknown.Int()
			}
			return ui.ExitBadConfig.Int()
		}
	}
	if err := config.SaveGlobal(path, &staged); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config set: save: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	logSetApplied(stderr, parsed)
	return ui.ExitOK.Int()
}

// errUnknownKey is the sentinel that applyGlobalKey returns when the
// requested key isn't in the global schema. Wrapped (errors.Is)
// rather than equality-compared so callers stay loose.
var errUnknownKey = errors.New("unknown key")

// applySandboxKey coerces and assigns one KEY=VALUE to a Sandbox
// struct. Per the comment on SettableKeys: if you add a key to
// SettableKeys, add its case here too.
//
// Per-field validation is "tight enough to refuse obvious garbage"
// — full validation runs once at the end of `config set`.
func applySandboxKey(s *config.Sandbox, key, raw string) error {
	switch key {
	case "host":
		if raw == "" {
			return fmt.Errorf("host: must be non-empty")
		}
		s.Host = raw
	case "port":
		p, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("port: %q: %w", raw, err)
		}
		if p < 1 || p > 65535 {
			return fmt.Errorf("port: %d out of range 1..65535", p)
		}
		s.Port = p
	case "superuser":
		if raw == "" {
			return fmt.Errorf("superuser: must be non-empty")
		}
		s.Superuser = raw
	case "defaultDatabase":
		if raw == "" {
			return fmt.Errorf("defaultDatabase: must be non-empty")
		}
		s.DefaultDatabase = raw
	case "binDir":
		if raw == "" || !filepath.IsAbs(raw) {
			return fmt.Errorf("binDir: must be a non-empty absolute path, got %q", raw)
		}
		s.BinDir = raw
	case "logFile":
		if raw == "" || !filepath.IsAbs(raw) {
			return fmt.Errorf("logFile: must be a non-empty absolute path, got %q", raw)
		}
		s.LogFile = raw
	default:
		// Unreachable: IsSettableKey already gated. Defensive.
		return fmt.Errorf("internal: applySandboxKey called with non-settable %q", key)
	}
	return nil
}

// applyGlobalKey is the analog for Global. The settable surface
// matches the Global fields: SPEC §3.3 calls all of these
// host-defaults, so unlike the per-sandbox case there's no
// system-managed exclusion list. We still gate by name so a typo
// fails loudly.
func applyGlobalKey(g *config.Global, key, raw string) error {
	switch key {
	case "sandboxRoot":
		g.SandboxRoot = raw
	case "defaultBinDir":
		g.DefaultBinDir = raw
	case "pgGatherDir":
		g.PgGatherDir = raw
	case "defaultPortBase":
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("defaultPortBase: %q: %w", raw, err)
		}
		if v < 1 || v > 65535 {
			return fmt.Errorf("defaultPortBase: %d out of range 1..65535", v)
		}
		g.DefaultPortBase = v
	case "defaultPortRange":
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("defaultPortRange: %q: %w", raw, err)
		}
		if v < 1 {
			return fmt.Errorf("defaultPortRange: %d must be >= 1", v)
		}
		g.DefaultPortRange = v
	default:
		return fmt.Errorf("%w: %q", errUnknownKey, key)
	}
	return nil
}

// logSetApplied emits one slog info line per applied key to stderr.
// We construct a logger here (rather than threading one from
// main.go) so this command's logging is self-contained. The format
// matches SPEC §4.6's "key=value, info level" diagnostic style.
func logSetApplied(stderr io.Writer, pairs []kvPair) {
	log := ui.NewLogger(stderr, slog.LevelInfo)
	for _, p := range pairs {
		log.Info("config set", "key", p.Key, "value", p.Raw)
	}
}

// ---------------------------------------------------------------- //
// config validate
// ---------------------------------------------------------------- //

// runConfigValidate implements `pg_sandbox config validate`. SPEC
// §6.7. Loads the file, runs Validate, prints "OK" on success or the
// ValidationError on failure.
func runConfigValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var sc scope
	parseScopeFlags(fs, &sc)
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if err := validateScope("validate", sc); err != nil {
		fmt.Fprintln(stderr, err)
		return ui.ExitUsage.Int()
	}

	if sc.global {
		// Global has no Validate function in the package surface —
		// LoadGlobal already gates on schemaVersion and unknown
		// fields, so "load succeeds" is the validation contract for
		// the global file. We surface that as OK / error here.
		path, err := config.GlobalConfigPath()
		if err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config validate: %v\n", err)
			return ui.ExitGeneric.Int()
		}
		if _, err := config.LoadGlobal(path); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config validate: %v\n", err)
			return ui.ExitBadConfig.Int()
		}
		fmt.Fprintln(stdout, "OK")
		return ui.ExitOK.Int()
	}

	sc.sandboxDir = resolveSandboxArg(sc.sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sc.sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox config validate: not a sandbox: %s\n", sc.sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	sb, err := config.LoadSandbox(sc.sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config validate: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	if err := config.Validate(sb); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config validate: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	fmt.Fprintln(stdout, "OK")
	return ui.ExitOK.Int()
}

// ---------------------------------------------------------------- //
// config migrate
// ---------------------------------------------------------------- //

// runConfigMigrate implements `pg_sandbox config migrate`. SPEC
// §6.7. Reads a legacy pg_sandbox.env file, overlays defaults for
// fields the legacy file didn't supply, validates, and saves the
// new pg_sandbox.json next to it.
func runConfigMigrate(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("config migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var (
		sandboxDir string
		replace    bool
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target directory containing pg_sandbox.env (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&replace, "replace", false, "Delete the legacy pg_sandbox.env after successful save")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if _, _, gErr := globals.Resolve(stderr); gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if sandboxDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox config migrate: --sandbox-dir is required")
		return ui.ExitUsage.Int()
	}

	legacyPath := filepath.Join(sandboxDir, "pg_sandbox.env")
	if _, err := os.Stat(legacyPath); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config migrate: no legacy file to migrate at %s\n", legacyPath)
		return ui.ExitNotASandbox.Int()
	}
	targetPath := filepath.Join(sandboxDir, config.SandboxFilename)
	if _, err := os.Stat(targetPath); err == nil {
		fmt.Fprintf(stderr, "pg_sandbox config migrate: target exists; refusing to overwrite %s\n", targetPath)
		return ui.ExitBadConfig.Int()
	}

	migrated, err := config.Migrate(legacyPath)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config migrate: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	// Overlay defaults for any field the legacy file didn't supply.
	// Migrate already set SchemaVersion; we touch only the still-
	// zero scalars.
	defaults := config.Defaults()
	if migrated.Host == "" {
		migrated.Host = defaults.Host
	}
	if migrated.Port == 0 {
		migrated.Port = defaults.Port
	}
	if migrated.Superuser == "" {
		migrated.Superuser = defaults.Superuser
	}
	if migrated.DefaultDatabase == "" {
		migrated.DefaultDatabase = defaults.DefaultDatabase
	}
	if migrated.Role == "" {
		migrated.Role = defaults.Role
	}

	if err := config.Validate(migrated); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config migrate: validation failed: %v\n", err)
		return ui.ExitBadConfig.Int()
	}
	if err := config.SaveSandbox(sandboxDir, migrated); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox config migrate: save: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	log := ui.NewLogger(stderr, slog.LevelInfo)
	log.Info("migrated", "from", legacyPath, "to", targetPath)

	if replace {
		if err := os.Remove(legacyPath); err != nil {
			fmt.Fprintf(stderr, "pg_sandbox config migrate: saved %s but could not remove %s: %v\n",
				targetPath, legacyPath, err)
			return ui.ExitGeneric.Int()
		}
	}
	return ui.ExitOK.Int()
}
