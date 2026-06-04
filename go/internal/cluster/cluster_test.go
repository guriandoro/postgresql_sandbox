// Unit tests for the cluster package.
//
// Strategy:
//
//   - We use pgexec.Fake so no real PostgreSQL is launched.
//
//   - For physical cluster Deploy, the primary's sandbox.Deploy
//     succeeds via the Fake; before fanning out to standbys we must
//     simulate "primary is running" so deployStandby's preflight
//     (isRunning + isPortListening) is satisfied. We drop a fake
//     pidfile and open a real TCP listener on the primary's port via
//     test helpers. Both are torn down on test exit.
//
//   - For logical cluster Deploy, the same trick applies. Additionally
//     Publish calls SHOW wal_level → we feed psql a stdout of
//     "logical\n" so the wal_level check passes without restart.
//
//   - For Status and Destroy, we deploy via the cluster package first
//     (or via a hand-rolled fixture) so the manifest and member dirs
//     exist on disk, then exercise the operation.

package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// ---------------------------------------------------------------- //
// memberName / physicalSlotName
// ---------------------------------------------------------------- //

func TestMemberNameConvention(t *testing.T) {
	cases := []struct {
		cluster string
		idx     int
		want    string
	}{
		{"foo", 0, "foo_p"},
		{"foo", 1, "foo_s1"},
		{"foo", 7, "foo_s7"},
	}
	for _, tc := range cases {
		got := memberName(tc.cluster, tc.idx)
		if got != tc.want {
			t.Errorf("memberName(%q,%d) = %q, want %q", tc.cluster, tc.idx, got, tc.want)
		}
	}
}

func TestPhysicalSlotNameConvention(t *testing.T) {
	got := physicalSlotName("mycluster", "mycluster_s1")
	want := "mycluster_mycluster_s1_slot"
	if got != want {
		t.Errorf("physicalSlotName: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------- //
// normalizeDeployOptions
// ---------------------------------------------------------------- //

func TestNormalizeDeployOptionsDefaults(t *testing.T) {
	opts := DeployOptions{
		ClusterDir: "/tmp/mycluster",
		BinDir:     "/opt/pg/bin",
		Nodes:      1,
	}
	if err := normalizeDeployOptions(&opts); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if opts.Mode != config.ClusterPhysical {
		t.Errorf("default mode: got %q, want %q", opts.Mode, config.ClusterPhysical)
	}
	if opts.SlotPrefix != "mycluster" {
		t.Errorf("default slot prefix: got %q, want %q", opts.SlotPrefix, "mycluster")
	}
	if opts.PubName != "pgs_pub" {
		t.Errorf("default pub name: got %q, want %q", opts.PubName, "pgs_pub")
	}
}

func TestNormalizeDeployOptionsErrors(t *testing.T) {
	cases := []struct {
		name string
		opts DeployOptions
		code ui.ExitCode
	}{
		{"missing cluster dir", DeployOptions{BinDir: "/opt/pg/bin", Nodes: 1}, ui.ExitUsage},
		{"missing bin dir", DeployOptions{ClusterDir: "/tmp/x", Nodes: 1}, ui.ExitUsage},
		{"missing nodes", DeployOptions{ClusterDir: "/tmp/x", BinDir: "/opt/pg/bin"}, ui.ExitUsage},
		{"negative sync", DeployOptions{ClusterDir: "/tmp/x", BinDir: "/opt/pg/bin", Nodes: 1, SyncCount: -1}, ui.ExitUsage},
		{"bad mode", DeployOptions{ClusterDir: "/tmp/x", BinDir: "/opt/pg/bin", Nodes: 1, Mode: "weird"}, ui.ExitUsage},
	}
	for _, tc := range cases {
		err := normalizeDeployOptions(&tc.opts)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if got := ExitCodeFor(err); got != tc.code {
			t.Errorf("%s: exit code: got %d, want %d", tc.name, got, tc.code)
		}
	}
}

// ---------------------------------------------------------------- //
// Cluster Deploy — physical
// ---------------------------------------------------------------- //

func TestDeployPhysicalHappyPath(t *testing.T) {
	root := t.TempDir()
	clusterDir := filepath.Join(root, "mycluster")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	// The pid-dropping Fake (see below) opens a real TCP listener on
	// the primary's port when it sees pg_ctl start, so the standby
	// preflight (isPortListening) is satisfied. We pick a free
	// ephemeral port via the kernel and use it explicitly so the
	// Fake's listener lands on a known address.
	primaryPort := freeProbePort(t)
	runner := &pidDroppingFake{}
	t.Cleanup(func() { runner.closeAllListeners() })

	_, err := Deploy(context.Background(), runner, DeployOptions{
		ClusterDir:   clusterDir,
		BinDir:       binDir,
		Host:         "127.0.0.1",
		Port:         primaryPort,
		PortExplicit: true,
		Nodes:        2,
		Mode:         config.ClusterPhysical,
		SelfPath:     "/usr/local/bin/pg_sandbox",
	}, io.Discard)
	if err != nil {
		t.Fatalf("cluster.Deploy: %v", err)
	}

	// Manifest should exist.
	if !config.IsClusterDir(clusterDir) {
		t.Fatalf("manifest missing under %s", clusterDir)
	}
	m, err := config.LoadCluster(clusterDir)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	if m.Mode != config.ClusterPhysical {
		t.Errorf("mode: got %q, want %q", m.Mode, config.ClusterPhysical)
	}
	if len(m.Members) != 3 {
		t.Fatalf("members: got %d, want 3", len(m.Members))
	}
	wantNames := []string{"mycluster_p", "mycluster_s1", "mycluster_s2"}
	wantRoles := []config.Role{config.RolePrimary, config.RoleStandby, config.RoleStandby}
	for i, want := range wantNames {
		if m.Members[i].Name != want {
			t.Errorf("member[%d] name: got %q, want %q", i, m.Members[i].Name, want)
		}
		if m.Members[i].Role != wantRoles[i] {
			t.Errorf("member[%d] role: got %q, want %q", i, m.Members[i].Role, wantRoles[i])
		}
	}
	if m.Replication.SlotPrefix != "mycluster" {
		t.Errorf("slot prefix: got %q, want %q", m.Replication.SlotPrefix, "mycluster")
	}

	// Each member dir should be a sandbox.
	for _, mb := range m.Members {
		dir := filepath.Join(clusterDir, mb.Name)
		if !config.IsSandboxDir(dir) {
			t.Errorf("member dir %s not a sandbox", dir)
		}
		// And each per-member config should carry Cluster=mycluster
		// so global_status can later group them.
		cfg, err := config.LoadSandbox(dir)
		if err != nil {
			t.Errorf("LoadSandbox(%s): %v", dir, err)
			continue
		}
		if cfg.Cluster != "mycluster" {
			t.Errorf("member %s Cluster=%q, want mycluster", mb.Name, cfg.Cluster)
		}
	}

	// At least one pg_basebackup call must have happened (standby
	// deploy uses it). Inspect runner.Calls.
	foundBaseback := false
	for _, c := range runner.Calls {
		if c.Name == "pg_basebackup" {
			foundBaseback = true
			break
		}
	}
	if !foundBaseback {
		t.Errorf("pg_basebackup never called; calls=%v", runner.Calls)
	}
}

func TestDeployRefusesExistingClusterDir(t *testing.T) {
	root := t.TempDir()
	clusterDir := filepath.Join(root, "mycluster")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	// Make the cluster dir non-empty so checkClusterDirAvailable
	// refuses.
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatalf("mkdir cluster: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatalf("stray: %v", err)
	}

	runner := &pidDroppingFake{}
	_, err := Deploy(context.Background(), runner, DeployOptions{
		ClusterDir:   clusterDir,
		BinDir:       binDir,
		Port:         freeProbePort(t),
		PortExplicit: true,
		Nodes:        1,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitClusterExists, got nil")
	}
	if got := ExitCodeFor(err); got != ui.ExitClusterExists {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitClusterExists)
	}
}

func TestDeploySyncCountWarns(t *testing.T) {
	root := t.TempDir()
	clusterDir := filepath.Join(root, "mycluster")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	primaryPort := freeProbePort(t)
	var stderr bytes.Buffer
	runner := &pidDroppingFake{}
	t.Cleanup(func() { runner.closeAllListeners() })
	_, err := Deploy(context.Background(), runner, DeployOptions{
		ClusterDir:   clusterDir,
		BinDir:       binDir,
		Host:         "127.0.0.1",
		Port:         primaryPort,
		PortExplicit: true,
		Nodes:        1,
		SyncCount:    1,
		Mode:         config.ClusterPhysical,
		SelfPath:     "/usr/local/bin/pg_sandbox",
	}, &stderr)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "synchronous") {
		t.Errorf("expected warn about synchronous; got: %s", stderr.String())
	}
}

// ---------------------------------------------------------------- //
// Cluster Deploy — logical
// ---------------------------------------------------------------- //

func TestDeployLogicalHappyPath(t *testing.T) {
	root := t.TempDir()
	clusterDir := filepath.Join(root, "mylog")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	primaryPort := freeProbePort(t)

	// For the Publish step we need SHOW wal_level to return "logical"
	// so no restart happens. The Fake returns the same canned Result
	// for every psql call, so "logical\n" satisfies wal_level (and
	// fails Atoi for max_replication_slots / max_wal_senders, which
	// skips raising those — benign).
	runner := &pidDroppingFake{}
	runner.SetResult("psql", pgexec.Result{Stdout: []byte("logical\n"), ExitCode: 0})
	t.Cleanup(func() { runner.closeAllListeners() })

	_, err := Deploy(context.Background(), runner, DeployOptions{
		ClusterDir:   clusterDir,
		BinDir:       binDir,
		Host:         "127.0.0.1",
		Port:         primaryPort,
		PortExplicit: true,
		Nodes:        2,
		Mode:         config.ClusterLogical,
		SelfPath:     "/usr/local/bin/pg_sandbox",
	}, io.Discard)
	if err != nil {
		t.Fatalf("logical Deploy: %v", err)
	}

	m, err := config.LoadCluster(clusterDir)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	if m.Mode != config.ClusterLogical {
		t.Errorf("mode: got %q, want %q", m.Mode, config.ClusterLogical)
	}
	if m.Replication.PublicationName != "pgs_pub" {
		t.Errorf("pub name: got %q, want pgs_pub", m.Replication.PublicationName)
	}
	if len(m.Members) != 3 {
		t.Fatalf("members: got %d, want 3", len(m.Members))
	}
	if m.Members[0].Role != config.RolePublisher {
		t.Errorf("member[0] role: got %q, want publisher", m.Members[0].Role)
	}
	for i := 1; i <= 2; i++ {
		if m.Members[i].Role != config.RoleSubscriber {
			t.Errorf("member[%d] role: got %q, want subscriber", i, m.Members[i].Role)
		}
	}

	// CREATE PUBLICATION must have been issued (Publish in logical
	// mode is called before subscribers deploy).
	foundPub := false
	for _, c := range runner.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "CREATE PUBLICATION pgs_pub") {
				foundPub = true
				break
			}
		}
	}
	if !foundPub {
		t.Errorf("CREATE PUBLICATION pgs_pub never issued; calls=%+v", runner.Calls)
	}
}

// ---------------------------------------------------------------- //
// Cluster Status
// ---------------------------------------------------------------- //

func TestStatusOnDeployedCluster(t *testing.T) {
	clusterDir, runner := deployPhysicalFixture(t, 2)

	rep, err := Status(context.Background(), runner, StatusOptions{ClusterDir: clusterDir}, io.Discard)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Name != "mycluster" {
		t.Errorf("cluster name: got %q, want mycluster", rep.Name)
	}
	if len(rep.Members) != 3 {
		t.Fatalf("members in status: got %d, want 3", len(rep.Members))
	}
	for i, m := range rep.Members {
		if m.Missing {
			t.Errorf("member[%d] %s reported missing", i, m.Name)
		}
		if m.Report == nil {
			t.Errorf("member[%d] %s no report", i, m.Name)
		}
	}

	// Text render contains a "cluster_name=mycluster" header line.
	var text bytes.Buffer
	rep.RenderText(&text)
	if !strings.Contains(text.String(), "cluster_name=mycluster") {
		t.Errorf("text render missing cluster header: %s", text.String())
	}
	if !strings.Contains(text.String(), "member=mycluster_p") {
		t.Errorf("text render missing primary block: %s", text.String())
	}

	// JSON render: parse it back into a generic map and assert on
	// the top-level shape.
	var jb bytes.Buffer
	if err := rep.RenderJSON(&jb); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(jb.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	if parsed["name"] != "mycluster" {
		t.Errorf("JSON name: got %v, want mycluster", parsed["name"])
	}
	if parsed["mode"] != "physical" {
		t.Errorf("JSON mode: got %v, want physical", parsed["mode"])
	}
	members, ok := parsed["members"].([]any)
	if !ok || len(members) != 3 {
		t.Errorf("JSON members: got %v", parsed["members"])
	}
}

func TestStatusMissingMember(t *testing.T) {
	clusterDir, runner := deployPhysicalFixture(t, 2)
	// Remove the last standby's dir to simulate a partial state.
	last := filepath.Join(clusterDir, "mycluster_s2")
	if err := os.RemoveAll(last); err != nil {
		t.Fatalf("rm: %v", err)
	}

	rep, err := Status(context.Background(), runner, StatusOptions{ClusterDir: clusterDir}, io.Discard)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	missing := 0
	for _, m := range rep.Members {
		if m.Missing {
			missing++
		}
	}
	if missing != 1 {
		t.Errorf("expected 1 missing member, got %d", missing)
	}
}

func TestStatusNotACluster(t *testing.T) {
	tmp := t.TempDir()
	runner := &pidDroppingFake{}
	_, err := Status(context.Background(), runner, StatusOptions{ClusterDir: tmp}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitNotACluster")
	}
	if got := ExitCodeFor(err); got != ui.ExitNotACluster {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitNotACluster)
	}
}

// ---------------------------------------------------------------- //
// Cluster Destroy
// ---------------------------------------------------------------- //

func TestDestroyHappyPath(t *testing.T) {
	clusterDir, runner := deployPhysicalFixture(t, 2)

	if err := Destroy(context.Background(), runner, DestroyOptions{ClusterDir: clusterDir}, io.Discard); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(clusterDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cluster dir still exists: err=%v", err)
	}
}

func TestDestroyReverseOrder(t *testing.T) {
	clusterDir, runner := deployPhysicalFixture(t, 2)
	// Reset call log so we only see destroy-time calls.
	runner.Calls = nil

	if err := Destroy(context.Background(), runner, DestroyOptions{ClusterDir: clusterDir}, io.Discard); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// Destroy calls pg_ctl stop only if the member's pidfile is
	// present (isRunning is true). Our fixture drops a pidfile via
	// pidDroppingFake when sandbox.Deploy ran pg_ctl start. We assert
	// that the order of "pg_ctl stop" invocations across calls runs
	// from s2 → s1 → p by inspecting -D argv (which points at the
	// member's data dir).
	var stopOrder []string
	for _, c := range runner.Calls {
		if c.Name != "pg_ctl" || len(c.Args) == 0 || c.Args[0] != "stop" {
			continue
		}
		for i, a := range c.Args {
			if a == "-D" && i+1 < len(c.Args) {
				stopOrder = append(stopOrder, filepath.Base(filepath.Dir(c.Args[i+1])))
				break
			}
		}
	}
	if len(stopOrder) < 3 {
		t.Fatalf("expected at least 3 stop calls in reverse order, got %v", stopOrder)
	}
	want := []string{"mycluster_s2", "mycluster_s1", "mycluster_p"}
	for i, w := range want {
		if i >= len(stopOrder) || stopOrder[i] != w {
			t.Errorf("stop order[%d]: got %q, want %q (full order=%v)", i, stopOrder[i], w, stopOrder)
		}
	}
}

func TestDestroyNotACluster(t *testing.T) {
	tmp := t.TempDir()
	runner := &pidDroppingFake{}
	err := Destroy(context.Background(), runner, DestroyOptions{ClusterDir: tmp}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitNotACluster")
	}
	if got := ExitCodeFor(err); got != ui.ExitNotACluster {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitNotACluster)
	}
}

func TestDestroyMissingMemberDirIsSkipped(t *testing.T) {
	clusterDir, runner := deployPhysicalFixture(t, 2)
	// Remove one standby's dir manually. Destroy should skip it and
	// still tear down the rest.
	if err := os.RemoveAll(filepath.Join(clusterDir, "mycluster_s2")); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := Destroy(context.Background(), runner, DestroyOptions{ClusterDir: clusterDir}, io.Discard); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(clusterDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cluster dir still exists: err=%v", err)
	}
}

// ---------------------------------------------------------------- //
// Test helpers
// ---------------------------------------------------------------- //

// pidDroppingFake is a pgexec.Fake that simulates side-effects a real
// pg_ctl start / stop would have. Cluster Deploy's standby preflight
// reads isRunning() (postmaster.pid present) AND isPortListening()
// (something bound to the configured port) — without simulating those,
// the second member's sandbox.Deploy would refuse the primary as
// "not running".
//
// On pg_ctl start (extracted from `-D <dir>` and `-o "-h H -p N"`):
//
//   - Drop a postmaster.pid into <dir> so isRunning is true.
//   - Open a TCP listener on the port from -o so isPortListening is
//     true. Track listeners by dir so a later pg_ctl stop can close.
//   - Drop a minimal pg_hba.conf so deploy_standby's pg_hba edit
//     works.
//
// On pg_ctl stop: remove the pidfile and close the matching
// listener.
type pidDroppingFake struct {
	pgexec.Fake
	listeners map[string]net.Listener
}

func (f *pidDroppingFake) Run(ctx context.Context, name string, args ...string) pgexec.Result {
	res := f.Fake.Run(ctx, name, args...)
	if name == "pg_ctl" && len(args) > 0 && args[0] == "start" {
		dataDir, host, port := parsePgCtlStart(args)
		if dataDir != "" {
			_ = os.MkdirAll(dataDir, 0o755)
			_ = os.WriteFile(filepath.Join(dataDir, "postmaster.pid"),
				[]byte("12345\n"), 0o600)
			hba := filepath.Join(dataDir, "pg_hba.conf")
			if _, err := os.Stat(hba); errors.Is(err, os.ErrNotExist) {
				_ = os.WriteFile(hba, []byte("local all all trust\n"), 0o600)
			}
		}
		if host != "" && port > 0 {
			// Bind a real listener so portalloc.IsBusy returns true.
			// Errors here are non-fatal: a port collision with another
			// test or with the actual primary listener (test setup)
			// just means the bind fails silently.
			if ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port))); err == nil {
				if f.listeners == nil {
					f.listeners = map[string]net.Listener{}
				}
				f.listeners[dataDir] = ln
			}
		}
	}
	if name == "pg_ctl" && len(args) > 0 && args[0] == "stop" {
		dataDir, _, _ := parsePgCtlStart(args)
		if dataDir != "" {
			_ = os.Remove(filepath.Join(dataDir, "postmaster.pid"))
			if ln, ok := f.listeners[dataDir]; ok {
				_ = ln.Close()
				delete(f.listeners, dataDir)
			}
		}
	}
	return res
}

// parsePgCtlStart extracts -D <dir> and -o "-h H -p N" from a pg_ctl
// start argv. Returns ("", "", 0) for missing pieces; callers tolerate.
func parsePgCtlStart(args []string) (dataDir, host string, port int) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-D":
			if i+1 < len(args) {
				dataDir = args[i+1]
			}
		case "-o":
			if i+1 < len(args) {
				// The -o value is a single string like "-h 127.0.0.1 -p 65432".
				fields := strings.Fields(args[i+1])
				for j := 0; j < len(fields); j++ {
					switch fields[j] {
					case "-h":
						if j+1 < len(fields) {
							host = fields[j+1]
						}
					case "-p":
						if j+1 < len(fields) {
							if n, err := strconv.Atoi(fields[j+1]); err == nil {
								port = n
							}
						}
					}
				}
			}
		}
	}
	return
}

// deployPhysicalFixture deploys a physical cluster with one primary
// and `standbys` standbys using the pid-dropping Fake. Returns the
// cluster dir and the runner so tests can either assert on calls or
// reuse the same runner for Status / Destroy.
//
// The primary's port is a kernel-assigned free port; we open a
// listener on it BEFORE Deploy and keep it open across the test so
// the standby preflight (isPortListening) is satisfied for every
// standby in the chain.
func deployPhysicalFixture(t *testing.T, standbys int) (string, *pidDroppingFake) {
	t.Helper()
	root := t.TempDir()
	clusterDir := filepath.Join(root, "mycluster")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	primaryPort := freeProbePort(t)
	runner := &pidDroppingFake{}
	t.Cleanup(func() { runner.closeAllListeners() })
	_, err := Deploy(context.Background(), runner, DeployOptions{
		ClusterDir:   clusterDir,
		BinDir:       binDir,
		Host:         "127.0.0.1",
		Port:         primaryPort,
		PortExplicit: true,
		Nodes:        standbys,
		Mode:         config.ClusterPhysical,
		SelfPath:     "/usr/local/bin/pg_sandbox",
	}, io.Discard)
	if err != nil {
		t.Fatalf("deployPhysicalFixture: %v", err)
	}
	return clusterDir, runner
}

// freeProbePort asks the kernel for an unused ephemeral port, closes
// the listener, and returns the number. Race window is tiny; for unit
// tests it's fine.
func freeProbePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// closeAllListeners shuts down every listener the pidDroppingFake has
// opened. Tests register this in t.Cleanup so background ports are
// released before the next test starts.
func (f *pidDroppingFake) closeAllListeners() {
	for k, ln := range f.listeners {
		_ = ln.Close()
		delete(f.listeners, k)
	}
}
