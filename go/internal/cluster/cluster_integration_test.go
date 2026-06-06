//go:build integration

// Integration smoke test for the cluster package: deploy a 3-node
// physical cluster against real PG binaries, walk Status, then
// Destroy. Build-tagged so the default `go test ./...` doesn't
// select it; opt in with PGS_BIN_DIR=/opt/postgresql/X.Y go test
// -tags=integration ./....

package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
)

// skipUnlessRealPG is the cluster-package twin of the sandbox helper.
// Kept local rather than imported so the integration build stays
// hermetic per-package and the helper compiles only under
// -tags=integration.
func skipUnlessRealPG(t *testing.T) string {
	t.Helper()
	binDir := os.Getenv("PGS_BIN_DIR")
	if binDir == "" {
		t.Skip("set PGS_BIN_DIR to run integration tests")
	}
	tried := []string{
		filepath.Join(binDir, "pg_ctl"),
		filepath.Join(binDir, "bin", "pg_ctl"),
		filepath.Join(binDir, "initdb"),
		filepath.Join(binDir, "bin", "initdb"),
	}
	foundPgCtl, foundInitdb := false, false
	for _, p := range tried {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		switch filepath.Base(p) {
		case "pg_ctl":
			foundPgCtl = true
		case "initdb":
			foundInitdb = true
		}
	}
	if !foundPgCtl || !foundInitdb {
		t.Skipf("PGS_BIN_DIR=%s does not contain pg_ctl/initdb: tried %v", binDir, tried)
	}
	if _, err := pgexec.New(binDir).Locate("pg_ctl"); err != nil {
		t.Skip(err.Error())
	}
	return binDir
}

// TestIntegrationClusterPhysical_DeployStatusDestroy spins up a
// 3-standby physical cluster (1 primary + 3 standbys), verifies
// Status reports every member running with the expected role mix,
// then Destroys and confirms the cluster dir is gone.
//
// NOTE: Nodes is the count of STANDBYS — member 0 (primary) is
// implicit. So Nodes=3 yields a 4-member cluster, which is more
// than the brief's "3 members" but matches the package contract.
// If a future revision wants exactly 3 members, change to Nodes=2.
func TestIntegrationClusterPhysical_DeployStatusDestroy(t *testing.T) {
	binDir := skipUnlessRealPG(t)
	runner := pgexec.New(binDir)

	clusterDir := filepath.Join(t.TempDir(), "mycluster")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	manifest, err := Deploy(ctx, runner, DeployOptions{
		ClusterDir: clusterDir,
		BinDir:     binDir,
		Nodes:      2, // total members = 3 (primary + 2 standbys)
		Mode:       config.ClusterPhysical,
	}, os.Stderr)
	if err != nil {
		t.Fatalf("cluster.Deploy: %v", err)
	}
	t.Cleanup(func() {
		// Use a fresh context: the outer ctx may have been cancelled
		// by a t.Fatal in the test body.
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanCancel()
		_ = Destroy(cleanCtx, runner, DestroyOptions{ClusterDir: clusterDir}, os.Stderr)
	})

	if manifest == nil {
		t.Fatal("cluster.Deploy: nil manifest")
	}
	if got, want := len(manifest.Members), 3; got != want {
		t.Errorf("manifest members: got %d, want %d", got, want)
	}

	// Status all members. Expect 1 primary + 2 standbys, all running.
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer statusCancel()
	cs, err := Status(statusCtx, runner, StatusOptions{ClusterDir: clusterDir}, os.Stderr)
	if err != nil {
		t.Fatalf("cluster.Status: %v", err)
	}
	primaries, standbys, running := 0, 0, 0
	for _, m := range cs.Members {
		if m.Missing || m.Report == nil {
			t.Errorf("member %q: missing=%t report=%v", m.Name, m.Missing, m.Report)
			continue
		}
		if m.Report.State == sandbox.RunStateRunning {
			running++
		} else {
			t.Errorf("member %q state: got %q, want %q",
				m.Name, m.Report.State, sandbox.RunStateRunning)
		}
		switch m.Role {
		case config.RolePrimary:
			primaries++
		case config.RoleStandby:
			standbys++
		}
	}
	if primaries != 1 {
		t.Errorf("primary count: got %d, want 1", primaries)
	}
	if standbys != 2 {
		t.Errorf("standby count: got %d, want 2", standbys)
	}
	if running != 3 {
		t.Errorf("running count: got %d, want 3", running)
	}

	// Destroy. We unregister the cleanup by destroying here, but
	// also leave the cleanup hook in place as a safety net (Destroy
	// on a missing dir returns ExitNotACluster, which the cleanup
	// swallows).
	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer destroyCancel()
	if err := Destroy(destroyCtx, runner, DestroyOptions{ClusterDir: clusterDir}, os.Stderr); err != nil {
		t.Fatalf("cluster.Destroy: %v", err)
	}
	if _, statErr := os.Stat(clusterDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("after Destroy, cluster dir stat err=%v, want os.ErrNotExist", statErr)
	}
}
