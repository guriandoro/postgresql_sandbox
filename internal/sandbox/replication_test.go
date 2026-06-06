// Tests for the physical-replication code path: standby deploy,
// promote, best-effort slot cleanup at destroy, and replication
// info in status.
//
// These tests use pgexec.Fake and t.TempDir(); none of them launch
// a real PostgreSQL. Source-side preparation (replicator role,
// pg_hba edit) is asserted by examining f.Calls and reading the
// fake pg_hba.conf back.

package sandbox

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// ---------------------------------------------------------------
// resolveSourceSandbox
// ---------------------------------------------------------------

func TestResolveSourceSandboxSibling(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	mustWriteEmptySandbox(t, src, "primary")

	got, err := resolveSourceSandbox(tgt, "primary")
	if err != nil {
		t.Fatalf("resolveSourceSandbox: %v", err)
	}
	if got != filepath.Clean(src) {
		t.Errorf("resolved path: got %q, want %q", got, src)
	}
}

func TestResolveSourceSandboxAbsolute(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	mustWriteEmptySandbox(t, src, "primary")

	got, err := resolveSourceSandbox(tgt, src)
	if err != nil {
		t.Fatalf("resolveSourceSandbox: %v", err)
	}
	if got != filepath.Clean(src) {
		t.Errorf("resolved path: got %q, want %q", got, src)
	}
}

func TestResolveSourceSandboxMissing(t *testing.T) {
	root := t.TempDir()
	tgt := filepath.Join(root, "standby1")
	_, err := resolveSourceSandbox(tgt, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitSourceUnreachable {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitSourceUnreachable)
	}
}

func TestResolveSourceSandboxEmptyName(t *testing.T) {
	_, err := resolveSourceSandbox("/tmp/foo", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

// ---------------------------------------------------------------
// Standby deploy
// ---------------------------------------------------------------

func TestDeployStandbyHappyPath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	// Make the source look like a fully-deployed running sandbox.
	srcPort := freeProbePort(t)
	makeRunningSourceFixture(t, src, "primary", binDir, srcPort)

	// Probe says replicator role does NOT exist yet (empty stdout
	// from the first psql call). We need the second psql call —
	// CREATE ROLE — to succeed. Both go through "psql" so we use the
	// default zero Result (exit 0, empty stdout) — that's exactly
	// what we want for both probe and create.
	f := &pgexec.Fake{}

	res, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    tgt,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary",
		SlotName:      "primary_standby1_slot",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Deploy standby: %v", err)
	}
	if res == nil || res.Sandbox == nil {
		t.Fatal("Deploy result missing")
	}
	if res.Sandbox.Role != config.RoleStandby {
		t.Errorf("role: got %q, want %q", res.Sandbox.Role, config.RoleStandby)
	}
	if res.Sandbox.Physical == nil {
		t.Fatal("Physical block missing")
	}
	if res.Sandbox.Physical.SlotName != "primary_standby1_slot" {
		t.Errorf("slot name: got %q", res.Sandbox.Physical.SlotName)
	}
	if res.Sandbox.Physical.SourceSandbox != "primary" {
		t.Errorf("source: got %q", res.Sandbox.Physical.SourceSandbox)
	}
	if res.Sandbox.Physical.ReplicationUser != "replicator" {
		t.Errorf("repl user: got %q", res.Sandbox.Physical.ReplicationUser)
	}

	// Sandbox file present.
	if !config.IsSandboxDir(tgt) {
		t.Errorf("standby dir not recognized after deploy")
	}

	// pg_basebackup must have been invoked with -R -X stream -C
	// --slot=…
	foundBasebackup := false
	for _, c := range f.Calls {
		if c.Name != "pg_basebackup" {
			continue
		}
		foundBasebackup = true
		if !containsString(c.Args, "-R") {
			t.Errorf("pg_basebackup missing -R: %v", c.Args)
		}
		if !containsString(c.Args, "stream") {
			t.Errorf("pg_basebackup missing -X stream: %v", c.Args)
		}
		if !containsString(c.Args, "--slot=primary_standby1_slot") {
			t.Errorf("pg_basebackup missing --slot=…: %v", c.Args)
		}
	}
	if !foundBasebackup {
		t.Errorf("pg_basebackup never called; calls=%v", f.Calls)
	}

	// pg_hba.conf on the source should have been appended.
	hba, err := os.ReadFile(filepath.Join(src, "data", "pg_hba.conf"))
	if err != nil {
		t.Fatalf("read pg_hba.conf: %v", err)
	}
	if !strings.Contains(string(hba), pgHbaReplicationLine) {
		t.Errorf("pg_hba.conf missing replication line; got: %s", hba)
	}

	// pg_ctl reload must have been issued on the source after the
	// pg_hba edit.
	foundReload := false
	for _, c := range f.Calls {
		if c.Name == "pg_ctl" && len(c.Args) > 0 && c.Args[0] == "reload" {
			foundReload = true
			break
		}
	}
	if !foundReload {
		t.Errorf("pg_ctl reload never issued after pg_hba edit; calls=%v", f.Calls)
	}

	// promote convenience script should be present.
	if _, err := os.Stat(filepath.Join(tgt, "promote")); err != nil {
		t.Errorf("promote script missing: %v", err)
	}
}

func TestDeployStandbyRequiresSlot(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPort := freeProbePort(t)
	makeRunningSourceFixture(t, src, "primary", binDir, srcPort)

	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    tgt,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary",
		// SlotName intentionally omitted.
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitUsage")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestDeployStandbySourceMissing(t *testing.T) {
	root := t.TempDir()
	tgt := filepath.Join(root, "standby1")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    tgt,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary", // no such sibling
		SlotName:      "slot1",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitSourceUnreachable")
	}
	if ExitCodeFor(err) != ui.ExitSourceUnreachable {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitSourceUnreachable)
	}
}

func TestDeployStandbySourceNotRunning(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Source exists but is NOT running (no pidfile and no listener).
	mustWriteSandboxFile(t, src, "primary", binDir, freeProbePort(t))

	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    tgt,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary",
		SlotName:      "slot1",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitSourceUnreachable")
	}
	if ExitCodeFor(err) != ui.ExitSourceUnreachable {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitSourceUnreachable)
	}
}

func TestDeployStandbyBasebackupFails(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPort := freeProbePort(t)
	makeRunningSourceFixture(t, src, "primary", binDir, srcPort)

	f := &pgexec.Fake{}
	f.SetResult("pg_basebackup", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("pg_basebackup: error: could not connect\n"),
	})
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    tgt,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary",
		SlotName:      "slot1",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitBasebackupFailed")
	}
	if ExitCodeFor(err) != ui.ExitBasebackupFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitBasebackupFailed)
	}
}

func TestDeployStandbyReplicatorAlreadyExists(t *testing.T) {
	// If the probe returns a non-empty stdout, we should NOT issue a
	// CREATE ROLE statement.
	root := t.TempDir()
	src := filepath.Join(root, "primary")
	tgt := filepath.Join(root, "standby1")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPort := freeProbePort(t)
	makeRunningSourceFixture(t, src, "primary", binDir, srcPort)

	f := &pgexec.Fake{}
	// Probe → "1" (role exists).
	f.SetResult("psql", pgexec.Result{Stdout: []byte("1\n"), ExitCode: 0})

	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    tgt,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary",
		SlotName:      "slot1",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// Inspect: NO psql call should contain "CREATE ROLE".
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "CREATE ROLE") {
				t.Errorf("CREATE ROLE issued even though probe said role exists; args=%v", c.Args)
			}
		}
	}
}

// ---------------------------------------------------------------
// Promote
// ---------------------------------------------------------------

func TestPromoteHappyPath(t *testing.T) {
	dir := standbyFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	// pg_ctl promote → success; psql pg_is_in_recovery → 'f'.
	f.SetResult("psql", pgexec.Result{Stdout: []byte("f\n"), ExitCode: 0})

	if err := Promote(context.Background(), f, PromoteOptions{SandboxDir: dir}, io.Discard); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Verify the config flipped.
	cfg2, err := config.LoadSandbox(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.Role != config.RolePrimary {
		t.Errorf("role: got %q, want %q", cfg2.Role, config.RolePrimary)
	}
	if cfg2.Physical != nil {
		t.Errorf("Physical block should be cleared; got %+v", cfg2.Physical)
	}

	// pg_ctl promote must have been called.
	if !callsContain(f, "pg_ctl", "promote") {
		t.Errorf("pg_ctl promote not called; calls=%v", f.Calls)
	}
}

func TestPromoteNotAStandby(t *testing.T) {
	dir := deployFixture(t) // role=primary
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	err := Promote(context.Background(), f, PromoteOptions{SandboxDir: dir}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitNotAStandby")
	}
	if ExitCodeFor(err) != ui.ExitNotAStandby {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotAStandby)
	}
}

func TestPromoteNotRunning(t *testing.T) {
	dir := standbyFixture(t)
	// No pidfile → not running → ExitNotAStandby (precondition).

	f := &pgexec.Fake{}
	err := Promote(context.Background(), f, PromoteOptions{SandboxDir: dir}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitNotAStandby {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotAStandby)
	}
}

func TestPromoteNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	err := Promote(context.Background(), f, PromoteOptions{SandboxDir: tmp}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestPromotePgctlFails(t *testing.T) {
	dir := standbyFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	f.SetResult("pg_ctl", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("pg_ctl: server did not promote\n"),
	})
	err := Promote(context.Background(), f, PromoteOptions{SandboxDir: dir}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitPromoteFailed")
	}
	if ExitCodeFor(err) != ui.ExitPromoteFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitPromoteFailed)
	}
}

// ---------------------------------------------------------------
// Destroy best-effort slot cleanup
// ---------------------------------------------------------------

func TestDestroyDropsSlotAtSource(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "primary")
	srcPort := freeProbePort(t)
	makeRunningSourceFixture(t, src, "primary", binDir, srcPort)

	// Make a fully-formed standby sandbox.
	stb := filepath.Join(root, "standby1")
	mustWriteStandbySandbox(t, stb, "standby1", binDir, freeProbePort(t),
		"primary", "primary_standby1_slot")

	// Source dir LIVE listener so isPortListening returns true. We
	// can't easily craft a sandbox with one open here because the
	// source fixture already binds one — leave that listener open by
	// holding the fixture in scope.

	f := &pgexec.Fake{}
	var stderr bytes.Buffer
	if err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: stb}, &stderr); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// Destroy should have invoked psql at the source with a
	// pg_drop_replication_slot statement.
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "pg_drop_replication_slot") &&
				strings.Contains(a, "primary_standby1_slot") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected psql pg_drop_replication_slot call; calls=%v", f.Calls)
	}
}

func TestDestroySlotCleanupSilentWhenSourceGone(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Standby references a source that doesn't exist on disk.
	stb := filepath.Join(root, "standby1")
	mustWriteStandbySandbox(t, stb, "standby1", binDir, freeProbePort(t),
		"primary", "primary_standby1_slot")

	f := &pgexec.Fake{}
	var stderr bytes.Buffer
	if err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: stb}, &stderr); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// No psql call should have been made (source not resolvable).
	for _, c := range f.Calls {
		if c.Name == "psql" {
			t.Errorf("unexpected psql call when source missing: %v", c)
		}
	}
	// A warning line should be present.
	if !strings.Contains(stderr.String(), "slot cleanup skipped") {
		t.Errorf("expected warning about skipped slot cleanup; stderr: %s", stderr.String())
	}
}

// ---------------------------------------------------------------
// Status replication info
// ---------------------------------------------------------------

func TestStatusPrimaryWithReplicas(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	// Listener for isPortListening.
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// First psql call is version probe; second is pg_stat_replication.
	// The Fake returns the SAME canned result for every call to a
	// given binary, so we need the pipe-delimited format to look like
	// a valid version string from probeVersion's perspective AND a
	// valid replicas row from probePrimaryReplication's. Since
	// probeVersion only takes the first line, we put the version on
	// line 1 and the replicas on line 2 — except probeVersion strips
	// after the first newline so it gets "PostgreSQL 16.2".
	// Meanwhile probePrimaryReplication uses -F'|' but the Fake doesn't
	// react to argv; it always returns this stdout. We need to give
	// callable, distinct outputs per-call. The Fake doesn't support
	// argv-conditional outputs out of the box, so we inspect what's
	// being asked via Calls instead and use the same stdout for both:
	// it's "standby1|streaming|async|||" — but that's NOT valid
	// version output. So instead, leave version empty and assert
	// only on the replicas section.
	f.SetResult("psql", pgexec.Result{
		Stdout:   []byte("standby1|streaming|async|||\n"),
		ExitCode: 0,
	})

	rep, err := Status(context.Background(), f, dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Replicas == nil {
		t.Fatalf("expected Replicas populated, got nil")
	}
	if len(rep.Replicas) != 1 {
		t.Fatalf("expected 1 replica, got %d: %+v", len(rep.Replicas), rep.Replicas)
	}
	if rep.Replicas[0].AppName != "standby1" {
		t.Errorf("AppName: got %q, want standby1", rep.Replicas[0].AppName)
	}
	if rep.Replicas[0].State != "streaming" {
		t.Errorf("State: got %q, want streaming", rep.Replicas[0].State)
	}

	var buf bytes.Buffer
	rep.RenderText(&buf)
	if !strings.Contains(buf.String(), "replicas[0]=app=standby1") {
		t.Errorf("RenderText missing replicas[0]; got: %s", buf.String())
	}
}

func TestStatusPrimaryNoReplicas(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// Empty stdout simulates "query ran, no rows".
	f.SetResult("psql", pgexec.Result{Stdout: []byte(""), ExitCode: 0})

	rep, err := Status(context.Background(), f, dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Replicas == nil {
		t.Fatalf("expected non-nil empty slice; got nil (=probe failed marker)")
	}
	if len(rep.Replicas) != 0 {
		t.Errorf("expected empty slice; got %v", rep.Replicas)
	}
	var buf bytes.Buffer
	rep.RenderText(&buf)
	if !strings.Contains(buf.String(), "replicas=(no connected replicas)") {
		t.Errorf("RenderText should show no-replicas line; got: %s", buf.String())
	}
}

func TestStatusStandbyShowsRecoveryAndReceiver(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stb := filepath.Join(root, "standby1")
	port := freeProbePort(t)
	mustWriteStandbySandbox(t, stb, "standby1", binDir, port, "primary", "primary_standby1_slot")
	cfg, _ := config.LoadSandbox(stb)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// The Fake returns the same Result for every "psql" call. We use
	// pipe-delimited wal_receiver shape; pg_is_in_recovery will then
	// see "streaming|0/A|…" and interpret it as not-"t", leaving
	// InRecovery false. To make the assertions matter we leave
	// InRecovery aside and focus on WalReceiver parsing.
	f.SetResult("psql", pgexec.Result{
		Stdout:   []byte("streaming|0/A|0/B|0/C|0/D\n"),
		ExitCode: 0,
	})

	rep, err := Status(context.Background(), f, stb)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.WalReceiver == nil {
		t.Fatal("expected WalReceiver populated")
	}
	if rep.WalReceiver.Status != "streaming" {
		t.Errorf("WalReceiver.Status: got %q", rep.WalReceiver.Status)
	}
	if rep.WalReceiver.ReceiveStartLSN != "0/A" {
		t.Errorf("WalReceiver.ReceiveStartLSN: got %q", rep.WalReceiver.ReceiveStartLSN)
	}
	if rep.Role != config.RoleStandby {
		t.Errorf("Role: got %q", rep.Role)
	}

	var buf bytes.Buffer
	rep.RenderText(&buf)
	if !strings.Contains(buf.String(), "wal_receiver=status=streaming") {
		t.Errorf("RenderText missing wal_receiver line; got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "role=standby") {
		t.Errorf("RenderText missing role=standby; got: %s", buf.String())
	}
}

func TestStatusReplicationProbeFailureWarnsButReturns(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// All psql calls fail.
	f.SetResult("psql", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("could not connect\n"),
	})

	var stderr bytes.Buffer
	rep, err := StatusWithStderr(context.Background(), f, dir, &stderr)
	if err != nil {
		t.Fatalf("Status returned error; should be best-effort: %v", err)
	}
	// Replicas nil = "probe failed" marker.
	if rep.Replicas != nil {
		t.Errorf("expected Replicas nil on probe failure; got %v", rep.Replicas)
	}
	if !strings.Contains(stderr.String(), "pg_stat_replication probe failed") {
		t.Errorf("expected warn line; got: %s", stderr.String())
	}
}

// ---------------------------------------------------------------
// Helpers specific to replication tests
// ---------------------------------------------------------------

// mustWriteEmptySandbox writes a minimal valid pg_sandbox.json into
// dir so config.IsSandboxDir(dir) returns true. Used by tests that
// only need a sandbox-shaped directory (not a runnable one).
func mustWriteEmptySandbox(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binDir := filepath.Join(filepath.Dir(dir), "_dummybin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	mustWriteSandboxFile(t, dir, name, binDir, 65432)
}

// mustWriteSandboxFile writes a config-valid Sandbox for `name` at
// `dir` so config.LoadSandbox succeeds. Data dir and log path are
// created relative to dir but no actual postgres bits exist. Useful
// for fixtures where Status/LoadSandbox need a real config but no
// process actually runs.
func mustWriteSandboxFile(t *testing.T, dir, name, binDir string, port int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Name = name
	cfg.BinDir = binDir
	cfg.DataDir = dataDir
	cfg.LogFile = filepath.Join(dir, "server.log")
	cfg.Port = port
	cfg.Role = config.RolePrimary
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := config.SaveSandbox(dir, &cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// mustWriteStandbySandbox is mustWriteSandboxFile for a standby —
// the Physical block is filled in and Role is set to standby so
// Validate accepts it.
func mustWriteStandbySandbox(t *testing.T, dir, name, binDir string, port int, sourceName, slot string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Name = name
	cfg.BinDir = binDir
	cfg.DataDir = dataDir
	cfg.LogFile = filepath.Join(dir, "server.log")
	cfg.Port = port
	cfg.Role = config.RoleStandby
	cfg.Physical = &config.Physical{
		SourceSandbox:   sourceName,
		SlotName:        slot,
		ReplicationUser: "replicator",
		SyncMode:        config.SyncNone,
		AppName:         name,
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := config.SaveSandbox(dir, &cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// makeRunningSourceFixture sets up a directory that LOOKS like a
// fully-deployed running sandbox: valid config, a postmaster.pid in
// the data dir, an open TCP listener on the configured port (so
// isPortListening returns true), and a pg_hba.conf so the standby
// deploy can read/edit it. Cleanup is registered with t.Cleanup so
// the listener is released at end of test.
func makeRunningSourceFixture(t *testing.T, dir, name, binDir string, port int) {
	t.Helper()
	mustWriteSandboxFile(t, dir, name, binDir, port)
	// pidfile so isRunning returns true
	mustCreatePid(t, filepath.Join(dir, "data"))
	// pg_hba.conf so ensureReplicationHba has something to read
	hba := filepath.Join(dir, "data", "pg_hba.conf")
	if err := os.WriteFile(hba, []byte("local all all trust\n"), 0o600); err != nil {
		t.Fatalf("pg_hba: %v", err)
	}
	// Listener so isPortListening returns true.
	ln := mustListenOn(t, "127.0.0.1", port)
	t.Cleanup(func() { _ = ln.Close() })
}

// mustListenOn opens a TCP listener on host:port and fails the test
// on error. Returns the listener for the caller to defer-close.
func mustListenOn(t *testing.T, host string, port int) interface{ Close() error } {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen %s:%d: %v", host, port, err)
	}
	return ln
}

// standbyFixture deploys a synthetic standby sandbox dir (config
// only; no real PG bits) so Promote tests have somewhere to operate.
func standbyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	dir := filepath.Join(root, "standby1")
	mustWriteStandbySandbox(t, dir, "standby1", binDir,
		freeProbePort(t), "primary", "primary_standby1_slot")
	return dir
}
