// Unit tests for global_status.
//
// Strategy:
//
//   - We build fixtures by writing pg_sandbox.json (and
//     pg_sandbox-cluster.json) directly under t.TempDir(). No
//     pgexec.Fake is needed because the walk reads only the on-disk
//     config; the running-state probe stats a pidfile and one TCP
//     port, both of which we drive deterministically (no file, no
//     listener → "stopped").
//
//   - For the running-state branch we drop a "postmaster.pid" into
//     the fixture's data dir and confirm State=crashed (no listener)
//     or running (listener bound). This mirrors the trick the cluster
//     tests use without pulling in their full pid-dropping Fake.
//
//   - Render-path tests assert on substrings rather than exact byte-
//     for-byte output. Column widths shift with name length and we
//     don't want a one-char fixture-name change to break the test.

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
)

// writeSandboxFixture lays down a minimal sandbox dir under root with
// the given name, port, cluster (optional). Returns the dir.
func writeSandboxFixture(t *testing.T, root, name string, port int, cluster string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.Defaults()
	cfg.Name = name
	cfg.BinDir = "/opt/pg/bin"
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.LogFile = filepath.Join(dir, "server.log")
	cfg.Host = "127.0.0.1"
	cfg.Port = port
	cfg.Role = config.RolePrimary
	cfg.Cluster = cluster
	cfg.CreatedAt = time.Now().UTC()
	if err := config.SaveSandbox(dir, &cfg); err != nil {
		t.Fatalf("SaveSandbox %s: %v", dir, err)
	}
	return dir
}

// writeClusterFixture lays down a cluster manifest + N member sandbox
// configs. Returns the cluster dir.
func writeClusterFixture(t *testing.T, root, name string, members []string, basePort int) string {
	t.Helper()
	cdir := filepath.Join(root, name)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatalf("mkdir cluster: %v", err)
	}
	m := &config.ClusterManifest{
		Name: name,
		Mode: config.ClusterPhysical,
	}
	for i, mn := range members {
		role := config.RolePrimary
		if i > 0 {
			role = config.RoleStandby
		}
		m.Members = append(m.Members, config.ClusterMember{Name: mn, Role: role})
		// Per-member sandbox config lives at <cluster>/<member>/.
		writeSandboxFixture(t, cdir, mn, basePort+i, name)
		// Overwrite role for non-primary members.
		mcfg, _ := config.LoadSandbox(filepath.Join(cdir, mn))
		mcfg.Role = role
		_ = config.SaveSandbox(filepath.Join(cdir, mn), mcfg)
	}
	if err := config.SaveCluster(cdir, m); err != nil {
		t.Fatalf("SaveCluster: %v", err)
	}
	return cdir
}

// writeClusterFixtureNamed is writeClusterFixture with the directory
// name decoupled from the manifest name. The walk visits cluster dirs
// in directory-name order but sorts gs.Clusters by manifest name, so a
// dirName that sorts differently from manifestName is what exercises
// the MED-12 pre-sort/post-sort index mismatch.
func writeClusterFixtureNamed(t *testing.T, root, dirName, manifestName string, members []string, basePort int) string {
	t.Helper()
	cdir := filepath.Join(root, dirName)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatalf("mkdir cluster: %v", err)
	}
	m := &config.ClusterManifest{
		Name: manifestName,
		Mode: config.ClusterPhysical,
	}
	for i, mn := range members {
		role := config.RolePrimary
		if i > 0 {
			role = config.RoleStandby
		}
		m.Members = append(m.Members, config.ClusterMember{Name: mn, Role: role})
		writeSandboxFixture(t, cdir, mn, basePort+i, manifestName)
		mcfg, _ := config.LoadSandbox(filepath.Join(cdir, mn))
		mcfg.Role = role
		_ = config.SaveSandbox(filepath.Join(cdir, mn), mcfg)
	}
	if err := config.SaveCluster(cdir, m); err != nil {
		t.Fatalf("SaveCluster: %v", err)
	}
	return cdir
}

// findCluster returns the ClusterEntry with the given manifest name, or
// nil. Returns a pointer into gs.Clusters so callers can inspect Members.
func findCluster(gs *GlobalStatus, name string) *ClusterEntry {
	for i := range gs.Clusters {
		if gs.Clusters[i].Name == name {
			return &gs.Clusters[i]
		}
	}
	return nil
}

// clusterHasMember reports whether the cluster lists a member of the
// given name.
func clusterHasMember(c *ClusterEntry, name string) bool {
	if c == nil {
		return false
	}
	for _, mb := range c.Members {
		if mb.Name == name {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- //
// Walk basics
// ----------------------------------------------------------------- //

func TestGlobalStatusEmptyRoot(t *testing.T) {
	root := t.TempDir()
	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 0 || len(gs.Clusters) != 0 || len(gs.Orphaned) != 0 {
		t.Errorf("expected empty result, got %+v", gs)
	}
	if gs.Root != root {
		t.Errorf("root: got %q, want %q", gs.Root, root)
	}
}

func TestGlobalStatusMissingRoot(t *testing.T) {
	// Path under TempDir that doesn't exist — walk must return an
	// empty result, NOT an error (first-run UX).
	root := filepath.Join(t.TempDir(), "does-not-exist")
	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 0 || len(gs.Clusters) != 0 {
		t.Errorf("expected empty result, got %+v", gs)
	}
}

func TestGlobalStatusSingleSandbox(t *testing.T) {
	root := t.TempDir()
	port := freeProbePort(t)
	writeSandboxFixture(t, root, "alpha", port, "")

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 1 {
		t.Fatalf("sandboxes: got %d, want 1", len(gs.Sandboxes))
	}
	sb := gs.Sandboxes[0]
	if sb.Name != "alpha" {
		t.Errorf("name: got %q, want alpha", sb.Name)
	}
	if sb.State != RunStateStopped {
		t.Errorf("state: got %q, want stopped", sb.State)
	}
	if sb.Port != port {
		t.Errorf("port: got %d, want %d", sb.Port, port)
	}
}

func TestGlobalStatusRunningState(t *testing.T) {
	root := t.TempDir()
	port := freeProbePort(t)
	dir := writeSandboxFixture(t, root, "running", port, "")
	// Drop a pidfile (with a live PID) so isRunning is true.
	mustCreatePid(t, filepath.Join(dir, "data"))
	// Bind a listener so isPortListening is true.
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 1 {
		t.Fatalf("sandboxes: got %d, want 1", len(gs.Sandboxes))
	}
	if gs.Sandboxes[0].State != RunStateRunning {
		t.Errorf("state: got %q, want running", gs.Sandboxes[0].State)
	}
}

func TestGlobalStatusCrashedState(t *testing.T) {
	// pidfile present, no listener → crashed.
	root := t.TempDir()
	port := freeProbePort(t)
	dir := writeSandboxFixture(t, root, "crashed", port, "")
	mustCreatePid(t, filepath.Join(dir, "data"))

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if gs.Sandboxes[0].State != RunStateCrashed {
		t.Errorf("state: got %q, want crashed", gs.Sandboxes[0].State)
	}
}

func TestGlobalStatusCluster(t *testing.T) {
	root := t.TempDir()
	basePort := freeProbePort(t)
	writeClusterFixture(t, root, "myc", []string{"myc_p", "myc_s1"}, basePort)

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 0 {
		t.Errorf("expected no top-level sandboxes (members are nested), got %d", len(gs.Sandboxes))
	}
	if len(gs.Clusters) != 1 {
		t.Fatalf("clusters: got %d, want 1", len(gs.Clusters))
	}
	c := gs.Clusters[0]
	if c.Name != "myc" {
		t.Errorf("cluster name: got %q, want myc", c.Name)
	}
	if len(c.Members) != 2 {
		t.Fatalf("members: got %d, want 2", len(c.Members))
	}
	if c.Members[0].Name != "myc_p" || c.Members[0].Role != config.RolePrimary {
		t.Errorf("member[0] mismatch: %+v", c.Members[0])
	}
	if c.Members[1].Name != "myc_s1" || c.Members[1].Role != config.RoleStandby {
		t.Errorf("member[1] mismatch: %+v", c.Members[1])
	}
	if c.Members[0].Cluster != "myc" {
		t.Errorf("member cluster name not stamped: %q", c.Members[0].Cluster)
	}
}

func TestGlobalStatusClusterMissingMember(t *testing.T) {
	root := t.TempDir()
	basePort := freeProbePort(t)
	cdir := writeClusterFixture(t, root, "myc", []string{"myc_p", "myc_s1"}, basePort)
	// Remove the standby dir to simulate partial deploy / destroy.
	if err := os.RemoveAll(filepath.Join(cdir, "myc_s1")); err != nil {
		t.Fatalf("rm: %v", err)
	}

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Clusters) != 1 {
		t.Fatalf("clusters: got %d", len(gs.Clusters))
	}
	c := gs.Clusters[0]
	if len(c.Members) != 2 {
		t.Fatalf("members (including missing placeholder): got %d, want 2", len(c.Members))
	}
	// Second member should be a placeholder entry: empty Host/Port.
	if c.Members[1].Host != "" || c.Members[1].Port != 0 {
		t.Errorf("missing member should have empty host/port, got %+v", c.Members[1])
	}
}

func TestGlobalStatusOrphan(t *testing.T) {
	// A sandbox claims membership in a cluster that isn't on disk.
	root := t.TempDir()
	writeSandboxFixture(t, root, "orphan_sb", freeProbePort(t), "ghost_cluster")

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 0 {
		t.Errorf("expected no top-level sandboxes (only an orphan), got %d", len(gs.Sandboxes))
	}
	if len(gs.Orphaned) != 1 {
		t.Fatalf("orphaned: got %d, want 1", len(gs.Orphaned))
	}
	if gs.Orphaned[0].Cluster != "ghost_cluster" {
		t.Errorf("orphan cluster name: got %q, want ghost_cluster", gs.Orphaned[0].Cluster)
	}
}

func TestGlobalStatusRelocatedMemberAttachedToCorrectClusterAfterSort(t *testing.T) {
	// Regression for MED-12. clusterByName records each cluster's index
	// as it is appended during the walk (directory-name order); gs.Clusters
	// is then sorted by manifest name; orphan reconciliation indexes the
	// SORTED slice with those PRE-sort positions. When walk order differs
	// from sorted order, a relocated sandbox that names an on-disk cluster
	// was appended to the wrong cluster's member list.
	//
	// Setup: dir-walk order (by dir name) is "dir_a" then "dir_b", but the
	// manifests inside them sort the other way — "zeta" then "alpha". A
	// relocated top-level sandbox names cluster "alpha" and must land under
	// alpha, never under zeta.
	root := t.TempDir()

	// dir "dir_a" (visited first) holds a cluster whose manifest is "zeta".
	writeClusterFixtureNamed(t, root, "dir_a", "zeta", []string{"zeta_p"}, freeProbePort(t))
	// dir "dir_b" (visited second) holds a cluster whose manifest is "alpha".
	writeClusterFixtureNamed(t, root, "dir_b", "alpha", []string{"alpha_p"}, freeProbePort(t))

	// A relocated sandbox living at the root that claims membership in the
	// on-disk cluster "alpha" (not nested under its cluster dir).
	writeSandboxFixture(t, root, "relocated_sb", freeProbePort(t), "alpha")

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(gs.Clusters) != 2 {
		t.Fatalf("clusters: got %d, want 2", len(gs.Clusters))
	}
	alpha := findCluster(gs, "alpha")
	zeta := findCluster(gs, "zeta")
	if alpha == nil || zeta == nil {
		t.Fatalf("expected clusters alpha and zeta, got %+v", gs.Clusters)
	}
	if !clusterHasMember(alpha, "relocated_sb") {
		t.Errorf("relocated_sb should be a member of cluster alpha; alpha members=%+v", alpha.Members)
	}
	if clusterHasMember(zeta, "relocated_sb") {
		t.Errorf("relocated_sb wrongly attached to cluster zeta; zeta members=%+v", zeta.Members)
	}
	// It must not linger as a top-level sandbox, and it names an on-disk
	// cluster so it must not be orphaned either.
	for _, sb := range gs.Sandboxes {
		if sb.Name == "relocated_sb" {
			t.Errorf("relocated_sb should have been moved into a cluster, still top-level")
		}
	}
	for _, sb := range gs.Orphaned {
		if sb.Name == "relocated_sb" {
			t.Errorf("relocated_sb names an on-disk cluster; should not be orphaned")
		}
	}
}

func TestGlobalStatusMixedRoot(t *testing.T) {
	// Standalone + a cluster, side-by-side.
	root := t.TempDir()
	writeSandboxFixture(t, root, "standalone_a", freeProbePort(t), "")
	writeSandboxFixture(t, root, "standalone_b", freeProbePort(t), "")
	writeClusterFixture(t, root, "myc", []string{"myc_p", "myc_s1"}, freeProbePort(t))

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(gs.Sandboxes) != 2 {
		t.Errorf("standalone count: got %d, want 2", len(gs.Sandboxes))
	}
	if len(gs.Clusters) != 1 {
		t.Errorf("cluster count: got %d, want 1", len(gs.Clusters))
	}
}

// ----------------------------------------------------------------- //
// Rendering
// ----------------------------------------------------------------- //

func TestRenderTextHeadersPresent(t *testing.T) {
	root := t.TempDir()
	writeSandboxFixture(t, root, "alpha", freeProbePort(t), "")
	writeClusterFixture(t, root, "myc", []string{"myc_p"}, freeProbePort(t))

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var buf bytes.Buffer
	gs.RenderText(&buf)
	out := buf.String()
	for _, want := range []string{"root=", "standalone sandboxes", "cluster: myc", "NAME", "STATE", "HOST:PORT"} {
		if !strings.Contains(out, want) {
			t.Errorf("text render missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderTextEmpty(t *testing.T) {
	root := t.TempDir()
	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var buf bytes.Buffer
	gs.RenderText(&buf)
	if !strings.Contains(buf.String(), "no sandboxes") {
		t.Errorf("empty render should mention 'no sandboxes': %s", buf.String())
	}
}

func TestRenderJSONShape(t *testing.T) {
	root := t.TempDir()
	writeSandboxFixture(t, root, "alpha", freeProbePort(t), "")
	writeClusterFixture(t, root, "myc", []string{"myc_p"}, freeProbePort(t))

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var buf bytes.Buffer
	if err := gs.RenderJSON(&buf); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON parse: %v\n%s", err, buf.String())
	}
	if _, ok := parsed["root"]; !ok {
		t.Errorf("JSON missing root")
	}
	if _, ok := parsed["sandboxes"]; !ok {
		t.Errorf("JSON missing sandboxes")
	}
	if _, ok := parsed["clusters"]; !ok {
		t.Errorf("JSON missing clusters")
	}
}

// ----------------------------------------------------------------- //
// Depth limiting
// ----------------------------------------------------------------- //

func TestWalkDepthLimit(t *testing.T) {
	// A sandbox buried deeper than globalWalkMaxDepth must NOT be
	// found. We construct a chain of empty intermediate dirs.
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c", "d", "e")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSandboxFixture(t, deep, "buried", freeProbePort(t), "")

	gs, err := GlobalStatusWalk(context.Background(), GlobalStatusOptions{Root: root}, io.Discard)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, sb := range gs.Sandboxes {
		if sb.Name == "buried" {
			t.Errorf("depth-limited walker found buried sandbox at depth>%d", globalWalkMaxDepth)
		}
	}
}

// freeProbePort is defined in sandbox_test.go and shared across
// this package's tests.
