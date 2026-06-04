// Tests for the configuration subsystem.
//
// Coverage breakdown:
//
//   - Defaults: returns a sane shape.
//   - ApplyEnv: each PGS_* var lands in the right field; PGS_PORT
//     typo errors loudly.
//   - SaveSandbox + LoadSandbox: round-trip equality.
//   - LoadSandbox: rejects unknown keys, rejects too-new schema.
//   - SaveSandbox: writes atomically (no torn file on a failed
//     write — we simulate by checking the temp file doesn't
//     linger).
//   - Validate: catches every documented failure category.
//   - Migrate: parses the legacy KEY=VALUE format including
//     comments / quoted values / export prefix.
//   - GlobalConfigPath: honors XDG_CONFIG_HOME.

package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mkSandbox is a test helper that builds a fully-populated valid
// Sandbox for a given sandbox dir. Used as the starting point for
// round-trip and validation tests.
func mkSandbox(dir string) *Sandbox {
	s := Defaults()
	s.Name = "test"
	s.BinDir = "/opt/postgresql/16.2/bin"
	s.DataDir = filepath.Join(dir, "data")
	s.LogFile = filepath.Join(dir, "server.log")
	s.Role = RolePrimary
	return &s
}

func TestDefaultsShape(t *testing.T) {
	d := Defaults()
	if d.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("Defaults.SchemaVersion = %d, want %d", d.SchemaVersion, CurrentSchemaVersion)
	}
	if d.Host != "127.0.0.1" {
		t.Errorf("Defaults.Host = %q, want 127.0.0.1", d.Host)
	}
	if d.Superuser != "postgres" || d.DefaultDatabase != "postgres" {
		t.Errorf("Defaults user/db: %q/%q, want postgres/postgres", d.Superuser, d.DefaultDatabase)
	}
	if d.Role != RoleUnknown {
		t.Errorf("Defaults.Role = %q, want unknown", d.Role)
	}
}

func TestApplyEnvOverlaysFields(t *testing.T) {
	env := map[string]string{
		"PGS_BIN_DIR": "/opt/pg",
		"PGS_HOST":    "0.0.0.0",
		"PGS_PORT":    "5433",
		"PGS_USER":    "dba",
		"PGS_DBNAME":  "appdb",
	}
	s, err := ApplyEnv(Defaults(), envGetter(env))
	if err != nil {
		t.Fatalf("ApplyEnv: %v", err)
	}
	if s.BinDir != "/opt/pg" || s.Host != "0.0.0.0" || s.Port != 5433 ||
		s.Superuser != "dba" || s.DefaultDatabase != "appdb" {
		t.Errorf("env overlay incomplete: %+v", s)
	}
}

func TestApplyEnvIgnoresUnset(t *testing.T) {
	s, err := ApplyEnv(Defaults(), envGetter(map[string]string{}))
	if err != nil {
		t.Fatalf("ApplyEnv: %v", err)
	}
	d := Defaults()
	if !reflect.DeepEqual(s, d) {
		t.Errorf("ApplyEnv with empty env modified the input: %+v vs %+v", s, d)
	}
}

func TestApplyEnvBadPort(t *testing.T) {
	_, err := ApplyEnv(Defaults(), envGetter(map[string]string{"PGS_PORT": "notanumber"}))
	if err == nil {
		t.Fatal("ApplyEnv with bad PGS_PORT: want error, got nil")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := mkSandbox(dir)
	if err := SaveSandbox(dir, in); err != nil {
		t.Fatalf("SaveSandbox: %v", err)
	}
	out, err := LoadSandbox(dir)
	if err != nil {
		t.Fatalf("LoadSandbox: %v", err)
	}
	// CreatedAt and LastModifiedAt are set by SaveSandbox; align
	// them on the input so DeepEqual compares the rest.
	in.CreatedAt = out.CreatedAt
	in.LastModifiedAt = out.LastModifiedAt
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SandboxFilename),
		[]byte(`{"schemaVersion":1,"thingThatDoesNotExist":42}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadSandbox(dir)
	if err == nil {
		t.Fatal("LoadSandbox accepted unknown field")
	}
	if !strings.Contains(err.Error(), "thingThatDoesNotExist") &&
		!strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error doesn't surface the unknown key: %v", err)
	}
}

func TestLoadRejectsFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SandboxFilename),
		[]byte(`{"schemaVersion":999,"name":"x"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadSandbox(dir)
	if err == nil {
		t.Fatal("LoadSandbox accepted too-new schemaVersion")
	}
	if !errors.Is(err, ErrSchemaVersionTooNew) {
		t.Errorf("error not ErrSchemaVersionTooNew: %v", err)
	}
}

func TestSaveAtomicLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	s := mkSandbox(dir)
	if err := SaveSandbox(dir, s); err != nil {
		t.Fatalf("SaveSandbox: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}
}

func TestValidateHappy(t *testing.T) {
	dir := t.TempDir()
	if err := Validate(mkSandbox(dir)); err != nil {
		t.Errorf("Validate happy path: %v", err)
	}
}

func TestValidateCatchesEveryProblem(t *testing.T) {
	bad := &Sandbox{
		// SchemaVersion 0, Name empty, BinDir empty,
		// DataDir relative, Port out of range, Role bogus,
		// LogFile empty, Host empty, Superuser/DefaultDatabase empty
		DataDir: "relative/path",
		Port:    99999,
		Role:    "bogus",
	}
	err := Validate(bad)
	if err == nil {
		t.Fatal("Validate accepted a clearly broken config")
	}
	v, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("Validate returned non-ValidationError: %T", err)
	}
	wantFragments := []string{
		"schemaVersion", "name", "binDir", "dataDir", "absolute",
		"logFile", "host", "port", "superuser", "defaultDatabase",
		"role",
	}
	joined := strings.Join(v.Problems, " | ")
	for _, frag := range wantFragments {
		if !strings.Contains(joined, frag) {
			t.Errorf("Validate didn't report %q; problems: %v", frag, v.Problems)
		}
	}
}

func TestValidatePhysicalRequiresBlock(t *testing.T) {
	dir := t.TempDir()
	s := mkSandbox(dir)
	s.Role = RoleStandby
	// No Physical block. Validate must complain.
	err := Validate(s)
	if err == nil {
		t.Fatal("Validate accepted standby without physical block")
	}
	if !strings.Contains(err.Error(), "physical") {
		t.Errorf("error doesn't mention physical: %v", err)
	}
}

func TestMigrateLegacyEnv(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "pg_sandbox.env")
	contents := `# legacy file from the Python tool
PGS_BIN_DIR=/opt/postgresql/16.2/bin
PGS_DATADIR=data
PGS_LOG=server.log
PGS_HOST="127.0.0.1"
PGS_PORT=65432
PGS_USER=postgres
PGS_DBNAME=postgres
PGS_ROLE=primary
export PGS_CLUSTER='mycluster'
`
	if err := os.WriteFile(legacy, []byte(contents), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	s, err := Migrate(legacy)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if s.BinDir != "/opt/postgresql/16.2/bin" {
		t.Errorf("BinDir: %q", s.BinDir)
	}
	if s.DataDir != filepath.Join(dir, "data") {
		t.Errorf("DataDir: %q", s.DataDir)
	}
	if s.LogFile != filepath.Join(dir, "server.log") {
		t.Errorf("LogFile: %q", s.LogFile)
	}
	if s.Host != "127.0.0.1" {
		t.Errorf("Host: %q (quotes should be stripped)", s.Host)
	}
	if s.Port != 65432 {
		t.Errorf("Port: %d", s.Port)
	}
	if s.Role != RolePrimary {
		t.Errorf("Role: %q", s.Role)
	}
	if s.Cluster != "mycluster" {
		t.Errorf("Cluster: %q (single quotes should be stripped)", s.Cluster)
	}
	if s.Name != filepath.Base(dir) {
		t.Errorf("Name: %q, want %q", s.Name, filepath.Base(dir))
	}
}

func TestMigratePhysicalBlock(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "pg_sandbox.env")
	if err := os.WriteFile(legacy, []byte(`
PGS_BIN_DIR=/opt/pg
PGS_DATADIR=data
PGS_LOG=server.log
PGS_HOST=127.0.0.1
PGS_PORT=65432
PGS_USER=postgres
PGS_DBNAME=postgres
PGS_ROLE=standby
PGS_REPLICATE_FROM=primary
PGS_SLOT_NAME=primary_standby1_slot
PGS_REPL_USER=replicator
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := Migrate(legacy)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if s.Physical == nil {
		t.Fatal("expected Physical block, got nil")
	}
	if s.Physical.SourceSandbox != "primary" ||
		s.Physical.SlotName != "primary_standby1_slot" ||
		s.Physical.ReplicationUser != "replicator" {
		t.Errorf("Physical block: %+v", s.Physical)
	}
}

func TestMigrateBadPort(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "pg_sandbox.env")
	os.WriteFile(legacy, []byte("PGS_PORT=NaN\n"), 0o644)
	if _, err := Migrate(legacy); err == nil {
		t.Error("Migrate accepted PGS_PORT=NaN")
	}
}

func TestGlobalConfigPathRespectsXDG(t *testing.T) {
	// Set XDG_CONFIG_HOME for the test. t.Setenv restores the
	// previous value (or unsetness) on test cleanup, so this is
	// safe alongside other tests.
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got, err := GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	want := filepath.Join("/xdg", GlobalDirname, GlobalFilename)
	if got != want {
		t.Errorf("GlobalConfigPath: got %q, want %q", got, want)
	}
}

func TestGlobalConfigPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/somewhere")
	got, err := GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	want := filepath.Join("/somewhere", ".config", GlobalDirname, GlobalFilename)
	if got != want {
		t.Errorf("GlobalConfigPath: got %q, want %q", got, want)
	}
}

func TestLoadGlobalMissingIsNotError(t *testing.T) {
	dir := t.TempDir()
	g, err := LoadGlobal(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Errorf("LoadGlobal missing: %v", err)
	}
	if g != nil {
		t.Errorf("LoadGlobal missing returned non-nil: %+v", g)
	}
}

func TestIsSandboxDir(t *testing.T) {
	dir := t.TempDir()
	if IsSandboxDir(dir) {
		t.Error("IsSandboxDir on empty dir = true")
	}
	if err := SaveSandbox(dir, mkSandbox(dir)); err != nil {
		t.Fatalf("SaveSandbox: %v", err)
	}
	if !IsSandboxDir(dir) {
		t.Error("IsSandboxDir on dir with config = false")
	}
}

// envGetter wraps a map into the env-lookup function shape that
// ApplyEnv accepts. Equivalent to os.Getenv backed by a fake map.
func envGetter(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
