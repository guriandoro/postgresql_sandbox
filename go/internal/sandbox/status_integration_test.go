//go:build integration

// Integration smoke test for sandbox.Status against a real-PG deploy.

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
)

// TestIntegrationStatus_AfterDeploy deploys a sandbox using real
// binaries and asserts Status reports a running instance. We use
// RunStateRunning as the "is it up?" signal because StatusReport
// has no Pid field — the running state is itself defined by the
// pidfile-present + port-listening pair (see status.go).
func TestIntegrationStatus_AfterDeploy(t *testing.T) {
	binDir := skipUnlessRealPG(t)
	runner := pgexec.New(binDir)

	sandboxDir := filepath.Join(t.TempDir(), "sb")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir: sandboxDir,
		BinDir:     binDir,
	}, os.Stderr); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { destroyIntegrationSandbox(t, runner, sandboxDir) })

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer statusCancel()
	rep, err := Status(statusCtx, runner, sandboxDir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep == nil {
		t.Fatal("Status returned nil report")
	}
	if rep.State != RunStateRunning {
		t.Errorf("State: got %q, want %q", rep.State, RunStateRunning)
	}
	if rep.Port <= 0 {
		t.Errorf("Port: got %d, want > 0", rep.Port)
	}
	// The pidfile-presence signal (which Status uses to compute
	// State=running) is the closest analog to the brief's "non-empty
	// Pid". We re-verify by stat'ing the postmaster.pid the deploy
	// produced so the assertion fails loudly if a future refactor
	// stops requiring it.
	pidPath := filepath.Join(rep.DataDir, "postmaster.pid")
	st, statErr := os.Stat(pidPath)
	if statErr != nil {
		t.Errorf("postmaster.pid stat: %v", statErr)
	} else if st.Size() == 0 {
		t.Errorf("postmaster.pid is empty")
	}
}
