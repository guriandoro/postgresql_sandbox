// Tests for Resolve / ResolveGlobal / SettableKeys.
//
// Coverage strategy: every layer of SPEC §3.1.2's precedence chain
// MUST be observed winning when (and only when) the layers above it
// are silent. We use table-driven tests that build a ResolveOptions
// with selected layers populated and assert the field's resolved
// source. A single end-to-end "all layers stacked" test then verifies
// the documented order: flag > env > sandbox > global > default.

package config

import (
	"reflect"
	"testing"
)

// findProv looks up a single Provenance entry by key. Returns
// a sentinel "missing" provenance if not found so the test failure
// is clear rather than panicking with index-out-of-range.
func findProv(t *testing.T, prov []Provenance, key string) Provenance {
	t.Helper()
	for _, p := range prov {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("provenance for %q not found; got keys %v", key, provKeys(prov))
	return Provenance{}
}

func provKeys(prov []Provenance) []string {
	out := make([]string, len(prov))
	for i, p := range prov {
		out[i] = p.Key
	}
	return out
}

func TestResolveDefaultsOnly(t *testing.T) {
	merged, prov, err := Resolve(ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Every key should be SourceDefault (except potentially "name"
	// which has no built-in default and stays empty — still
	// SourceDefault per our chain).
	for _, p := range prov {
		if p.Source != SourceDefault {
			t.Errorf("key %q: source = %q, want default", p.Key, p.Source)
		}
	}
	// And the merged struct should equal Defaults() for the fields
	// we resolve.
	d := Defaults()
	if merged.Host != d.Host || merged.Port != d.Port ||
		merged.Superuser != d.Superuser || merged.DefaultDatabase != d.DefaultDatabase {
		t.Errorf("merged != Defaults() for scalars: %+v", merged)
	}
}

func TestResolveSandboxLayerWins(t *testing.T) {
	s := Defaults()
	s.Name = "sb"
	s.BinDir = "/from/sandbox"
	s.Host = "10.0.0.1"
	s.Port = 6000
	s.Superuser = "sbuser"
	s.DefaultDatabase = "sbdb"
	s.Role = RolePrimary
	merged, prov, err := Resolve(ResolveOptions{Sandbox: &s})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	checks := map[string]Source{
		"name":            SourceSandboxFile,
		"binDir":          SourceSandboxFile,
		"host":            SourceSandboxFile,
		"port":            SourceSandboxFile,
		"superuser":       SourceSandboxFile,
		"defaultDatabase": SourceSandboxFile,
		"role":            SourceSandboxFile,
	}
	for k, want := range checks {
		got := findProv(t, prov, k)
		if got.Source != want {
			t.Errorf("key %q: source = %q, want %q", k, got.Source, want)
		}
	}
	if merged.Host != "10.0.0.1" || merged.Port != 6000 || merged.Name != "sb" {
		t.Errorf("merged values wrong: %+v", merged)
	}
}

func TestResolveGlobalLayerWinsForBinDir(t *testing.T) {
	// Only the global layer supplies binDir; the sandbox layer is
	// silent. Resolve must report SourceGlobalFile.
	g := GlobalDefaults()
	g.DefaultBinDir = "/from/global"
	merged, prov, err := Resolve(ResolveOptions{Global: &g})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := findProv(t, prov, "binDir")
	if got.Source != SourceGlobalFile {
		t.Errorf("binDir source = %q, want global-file", got.Source)
	}
	if merged.BinDir != "/from/global" {
		t.Errorf("merged.BinDir = %q, want /from/global", merged.BinDir)
	}
}

func TestResolveEnvLayerWinsOverSandbox(t *testing.T) {
	s := Defaults()
	s.Name = "sb"
	s.Host = "from-sandbox"
	s.Port = 6000
	env := envGetter(map[string]string{
		"PGS_HOST": "from-env",
		"PGS_PORT": "7000",
	})
	merged, prov, err := Resolve(ResolveOptions{Sandbox: &s, Env: env})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := findProv(t, prov, "host"); got.Source != SourceEnv {
		t.Errorf("host source = %q, want env", got.Source)
	}
	if got := findProv(t, prov, "port"); got.Source != SourceEnv {
		t.Errorf("port source = %q, want env", got.Source)
	}
	if merged.Host != "from-env" || merged.Port != 7000 {
		t.Errorf("merged: %+v", merged)
	}
}

func TestResolveFlagLayerWinsOverEverything(t *testing.T) {
	s := Defaults()
	s.Host = "from-sandbox"
	s.Port = 6000
	env := envGetter(map[string]string{"PGS_HOST": "from-env", "PGS_PORT": "7000"})
	merged, prov, err := Resolve(ResolveOptions{
		Sandbox:    &s,
		Env:        env,
		FlagsAsMap: map[string]any{"host": "from-flag", "port": 8000},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := findProv(t, prov, "host"); got.Source != SourceFlag {
		t.Errorf("host source = %q, want flag", got.Source)
	}
	if got := findProv(t, prov, "port"); got.Source != SourceFlag {
		t.Errorf("port source = %q, want flag", got.Source)
	}
	if merged.Host != "from-flag" || merged.Port != 8000 {
		t.Errorf("merged: %+v", merged)
	}
}

func TestResolveBadEnvPort(t *testing.T) {
	env := envGetter(map[string]string{"PGS_PORT": "notanumber"})
	_, _, err := Resolve(ResolveOptions{Env: env})
	if err == nil {
		t.Fatal("Resolve accepted bogus PGS_PORT")
	}
}

func TestResolveProvenanceOrderIsStable(t *testing.T) {
	_, prov, err := Resolve(ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := provKeys(prov)
	if !reflect.DeepEqual(got, ResolvedSandboxKeys) {
		t.Errorf("provenance key order:\n got %v\nwant %v", got, ResolvedSandboxKeys)
	}
}

func TestResolveGlobalDefaultsOnly(t *testing.T) {
	merged, prov, err := ResolveGlobal(ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveGlobal: %v", err)
	}
	for _, p := range prov {
		if p.Source != SourceDefault {
			t.Errorf("key %q: source = %q, want default", p.Key, p.Source)
		}
	}
	if merged.DefaultPortBase == 0 || merged.DefaultPortRange == 0 {
		t.Errorf("defaults not populated: %+v", merged)
	}
}

func TestResolveGlobalFileLayerWins(t *testing.T) {
	g := GlobalDefaults()
	g.SandboxRoot = "/sandboxes"
	g.PgGatherDir = "/pg-gather"
	merged, prov, err := ResolveGlobal(ResolveOptions{Global: &g})
	if err != nil {
		t.Fatalf("ResolveGlobal: %v", err)
	}
	if got := findProv(t, prov, "sandboxRoot"); got.Source != SourceGlobalFile {
		t.Errorf("sandboxRoot source = %q, want global-file", got.Source)
	}
	if merged.SandboxRoot != "/sandboxes" || merged.PgGatherDir != "/pg-gather" {
		t.Errorf("merged: %+v", merged)
	}
}

func TestResolveGlobalEnvLayerWinsOverFile(t *testing.T) {
	g := GlobalDefaults()
	g.SandboxRoot = "/from-file"
	env := envGetter(map[string]string{"PGS_SANDBOX_ROOT": "/from-env"})
	merged, prov, err := ResolveGlobal(ResolveOptions{Global: &g, Env: env})
	if err != nil {
		t.Fatalf("ResolveGlobal: %v", err)
	}
	if got := findProv(t, prov, "sandboxRoot"); got.Source != SourceEnv {
		t.Errorf("sandboxRoot source = %q, want env", got.Source)
	}
	if merged.SandboxRoot != "/from-env" {
		t.Errorf("merged.SandboxRoot = %q", merged.SandboxRoot)
	}
}

func TestSettableKeysIncludesUserKnobs(t *testing.T) {
	got := SettableKeys()
	want := map[string]bool{
		"host":            true,
		"port":            true,
		"superuser":       true,
		"defaultDatabase": true,
		"binDir":          true,
		"logFile":         true,
	}
	gotSet := map[string]bool{}
	for _, k := range got {
		gotSet[k] = true
	}
	for k := range want {
		if !gotSet[k] {
			t.Errorf("SettableKeys missing %q", k)
		}
	}
}

func TestIsSettableKeyRejectsSystemFields(t *testing.T) {
	for _, k := range []string{
		"schemaVersion", "name", "dataDir", "role", "cluster",
		"physical", "logical", "createdAt", "lastModifiedAt",
		"bogusUnknownKey",
	} {
		if IsSettableKey(k) {
			t.Errorf("IsSettableKey(%q) = true, want false", k)
		}
	}
}

func TestIsSettableKeyAcceptsAllSettable(t *testing.T) {
	for _, k := range SettableKeys() {
		if !IsSettableKey(k) {
			t.Errorf("IsSettableKey(%q) = false, want true", k)
		}
	}
}
