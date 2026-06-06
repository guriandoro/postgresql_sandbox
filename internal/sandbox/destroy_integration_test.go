//go:build integration

// Integration smoke test for sandbox.Destroy.

package sandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// TestIntegrationDestroy_RemovesEverything deploys a real sandbox,
// destroys it, then asserts (a) the sandbox dir is gone and (b) the
// port the instance held is free again — the latter catches the
// regression where Destroy returns success without actually
// terminating postgres.
func TestIntegrationDestroy_RemovesEverything(t *testing.T) {
	binDir := skipUnlessRealPG(t)
	runner := pgexec.New(binDir)

	sandboxDir := filepath.Join(t.TempDir(), "sb")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir: sandboxDir,
		BinDir:     binDir,
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	port := res.Sandbox.Port
	host := res.Sandbox.Host

	destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer destroyCancel()
	if err := Destroy(destroyCtx, runner, DestroyOptions{SandboxDir: sandboxDir}, os.Stderr); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if _, statErr := os.Stat(sandboxDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("after Destroy, sandbox dir stat err=%v, want os.ErrNotExist", statErr)
	}

	// The recorded port should be free again. Poll briefly: on some
	// kernels TIME_WAIT can hold the port for a moment after a clean
	// stop. Bound the wait so failure mode is "free quickly" not
	// "test hangs forever".
	if !waitPortFree(host, port, 5*time.Second) {
		t.Errorf("port %s:%d still busy after Destroy", host, port)
	}
}

// waitPortFree polls net.Listen on host:port until the bind succeeds
// or the deadline expires. Returns true on first successful bind
// (and closes the listener immediately so the port is free again
// for the next caller). The polling step is bounded at 100ms.
func waitPortFree(host string, port int, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			_ = ln.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
