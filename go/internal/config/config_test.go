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

// mkStandbySandbox returns a valid standby Sandbox with a fully
// populated Physical block. Per-field tests mutate one field at a
// time to introduce a single defect.
func mkStandbySandbox(dir string) *Sandbox {
	s := mkSandbox(dir)
	s.Role = RoleStandby
	s.Physical = &Physical{
		SourceSandbox:   "primary",
		SlotName:        "primary_standby1_slot",
		ReplicationUser: "replicator",
		SyncMode:        SyncNone,
		AppName:         "standby1",
	}
	return s
}

// mkSubscriberSandbox returns a valid subscriber Sandbox with a
// fully populated Logical block. Per-field tests mutate one field.
func mkSubscriberSandbox(dir string) *Sandbox {
	s := mkSandbox(dir)
	s.Role = RoleSubscriber
	s.Logical = &Logical{
		SourceSandbox:    "publisher",
		PublicationName:  "pub",
		SubscriptionName: "sub",
		CopyMode:         CopyAll,
		TargetDatabase:   "appdb",
	}
	return s
}

// assertValidationContains is a tiny helper to keep the per-field
// tests concise: run Validate, assert it returned a *ValidationError,
// and check that each fragment appears in the joined problem list.
func assertValidationContains(t *testing.T, err error, fragments ...string) *ValidationError {
	t.Helper()
	if err == nil {
		t.Fatal("Validate accepted a config it should have rejected")
	}
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("Validate returned non-ValidationError: %T (%v)", err, err)
	}
	joined := strings.Join(v.Problems, " | ")
	for _, frag := range fragments {
		if !strings.Contains(joined, frag) {
			t.Errorf("Validate didn't report %q; problems: %v", frag, v.Problems)
		}
	}
	return v
}

func TestValidatePhysicalMissingSourceSandbox(t *testing.T) {
	dir := t.TempDir()
	s := mkStandbySandbox(dir)
	s.Physical.SourceSandbox = ""
	assertValidationContains(t, Validate(s), "physical.sourceSandbox")
}

func TestValidatePhysicalMissingSlotName(t *testing.T) {
	dir := t.TempDir()
	s := mkStandbySandbox(dir)
	s.Physical.SlotName = ""
	assertValidationContains(t, Validate(s), "physical.slotName")
}

func TestValidatePhysicalMissingReplicationUser(t *testing.T) {
	dir := t.TempDir()
	s := mkStandbySandbox(dir)
	s.Physical.ReplicationUser = ""
	assertValidationContains(t, Validate(s), "physical.replicationUser")
}

func TestValidatePhysicalInvalidSyncMode(t *testing.T) {
	dir := t.TempDir()
	s := mkStandbySandbox(dir)
	s.Physical.SyncMode = SyncMode("weird")
	assertValidationContains(t, Validate(s), "physical.syncMode", "weird")
}

func TestValidateLogicalMissingSourceSandbox(t *testing.T) {
	dir := t.TempDir()
	s := mkSubscriberSandbox(dir)
	s.Logical.SourceSandbox = ""
	assertValidationContains(t, Validate(s), "logical.sourceSandbox")
}

func TestValidateLogicalMissingPublicationName(t *testing.T) {
	dir := t.TempDir()
	s := mkSubscriberSandbox(dir)
	s.Logical.PublicationName = ""
	assertValidationContains(t, Validate(s), "logical.publicationName")
}

func TestValidateLogicalMissingSubscriptionName(t *testing.T) {
	dir := t.TempDir()
	s := mkSubscriberSandbox(dir)
	s.Logical.SubscriptionName = ""
	assertValidationContains(t, Validate(s), "logical.subscriptionName")
}

func TestValidateLogicalMissingTargetDatabase(t *testing.T) {
	dir := t.TempDir()
	s := mkSubscriberSandbox(dir)
	s.Logical.TargetDatabase = ""
	assertValidationContains(t, Validate(s), "logical.targetDatabase")
}

func TestValidateLogicalInvalidCopyMode(t *testing.T) {
	dir := t.TempDir()
	s := mkSubscriberSandbox(dir)
	s.Logical.CopyMode = CopyMode("weird")
	assertValidationContains(t, Validate(s), "logical.copyMode", "weird")
}

func TestValidateSubscriberRequiresLogicalBlock(t *testing.T) {
	dir := t.TempDir()
	s := mkSandbox(dir)
	s.Role = RoleSubscriber
	// No Logical block. Validate must complain.
	assertValidationContains(t, Validate(s), "subscriber", "logical")
}

func TestValidateAccumulatesPhysicalProblems(t *testing.T) {
	dir := t.TempDir()
	s := mkStandbySandbox(dir)
	// Two simultaneous defects: missing SourceSandbox AND invalid
	// SyncMode. Both must appear; Validate must not short-circuit.
	s.Physical.SourceSandbox = ""
	s.Physical.SyncMode = SyncMode("weird")
	v := assertValidationContains(t, Validate(s), "physical.sourceSandbox", "physical.syncMode")
	if len(v.Problems) < 2 {
		t.Errorf("expected >=2 problems, got %d: %v", len(v.Problems), v.Problems)
	}
}

func TestValidateAccumulatesLogicalProblems(t *testing.T) {
	dir := t.TempDir()
	s := mkSubscriberSandbox(dir)
	// Two simultaneous defects: missing PublicationName AND invalid
	// CopyMode. Both must surface in one pass.
	s.Logical.PublicationName = ""
	s.Logical.CopyMode = CopyMode("weird")
	v := assertValidationContains(t, Validate(s), "logical.publicationName", "logical.copyMode")
	if len(v.Problems) < 2 {
		t.Errorf("expected >=2 problems, got %d: %v", len(v.Problems), v.Problems)
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

// ---------------------------------------------------------------- //
// Cluster manifest round-trip + strictness
// ---------------------------------------------------------------- //

// mkClusterManifest returns a fully-populated physical-mode manifest
// suitable for round-trip and validation tests. SyncIndex is exercised
// on member 0 (sync) and left nil on member 1 (async) so the pointer
// semantics get coverage.
func mkClusterManifest(name string) *ClusterManifest {
	syncIdx := 0
	return &ClusterManifest{
		SchemaVersion: CurrentSchemaVersion,
		Name:          name,
		Mode:          ClusterPhysical,
		Members: []ClusterMember{
			{Name: name + "_p", Role: RolePrimary, SyncIndex: nil},
			{Name: name + "_s1", Role: RoleStandby, SyncIndex: &syncIdx},
			{Name: name + "_s2", Role: RoleStandby, SyncIndex: nil},
		},
		Replication: ClusterRepl{
			SlotPrefix: name,
			SyncCount:  0,
		},
	}
}

func TestClusterSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := mkClusterManifest("mycluster")
	if err := SaveCluster(dir, in); err != nil {
		t.Fatalf("SaveCluster: %v", err)
	}
	out, err := LoadCluster(dir)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	// Save populated CreatedAt / LastModifiedAt; align so DeepEqual
	// compares the rest.
	in.CreatedAt = out.CreatedAt
	in.LastModifiedAt = out.LastModifiedAt
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

func TestClusterLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ClusterFilename),
		[]byte(`{"schemaVersion":1,"name":"x","mode":"physical","mystery":"key"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadCluster(dir)
	if err == nil {
		t.Fatal("LoadCluster accepted unknown field")
	}
	if !strings.Contains(err.Error(), "mystery") &&
		!strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error doesn't surface the unknown key: %v", err)
	}
}

func TestClusterLoadMissingIsError(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadCluster(dir)
	if err == nil {
		t.Fatal("LoadCluster on empty dir: expected error, got nil")
	}
	// Missing-file should bubble up as a PathError (os.ErrNotExist),
	// not as a JSON decoding error. Callers use this to distinguish
	// "not a cluster" (ENOENT) from "cluster file is broken".
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got: %v", err)
	}
}

func TestClusterLoadRejectsFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ClusterFilename),
		[]byte(`{"schemaVersion":999,"name":"x","mode":"physical","members":[],"replication":{"syncCount":0}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadCluster(dir)
	if err == nil {
		t.Fatal("LoadCluster accepted too-new schemaVersion")
	}
	if !errors.Is(err, ErrSchemaVersionTooNew) {
		t.Errorf("error not ErrSchemaVersionTooNew: %v", err)
	}
}

func TestIsClusterDir(t *testing.T) {
	dir := t.TempDir()
	if IsClusterDir(dir) {
		t.Error("IsClusterDir on empty dir = true")
	}
	if err := SaveCluster(dir, mkClusterManifest("x")); err != nil {
		t.Fatalf("SaveCluster: %v", err)
	}
	if !IsClusterDir(dir) {
		t.Error("IsClusterDir on dir with manifest = false")
	}
}
