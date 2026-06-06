//go:build integration

// Integration smoke tests for sandbox.Deploy that drive real PG
// binaries (initdb / pg_ctl / postgres / psql) discovered via
// PGS_BIN_DIR. The build tag keeps these out of the default
// `go test ./...` invocation; users opt in with
// `PGS_BIN_DIR=/opt/postgresql/18.4 go test -tags=integration ./...`.
// When PGS_BIN_DIR is unset or doesn't point at a usable install,
// every test in this file SKIPs with a clear message.

package sandbox

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
)

// skipUnlessRealPG returns the resolved PG bin-dir from PGS_BIN_DIR
// or t.Skips when the env is unset or doesn't contain pg_ctl/initdb.
// Local to the sandbox package so the integration build doesn't pull
// in a cross-package helper file (and so the helper compiles only
// under -tags=integration).
//
// Resolution order matches Locate in pgexec: check BinDir/pg_ctl,
// BinDir/bin/pg_ctl, BinDir/initdb, BinDir/bin/initdb. As a final
// cross-check we call pgexec.New(binDir).Locate("pg_ctl") so any
// gap in the lookup logic surfaces here rather than in a downstream
// test failure.
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

// freeIntegrationPort grabs an ephemeral port from the kernel, closes
// the listener, and returns the number. Same idiom as freeProbePort
// in sandbox_test.go (unit-tier) — duplicated here because that helper
// lives under the default build, and we don't want to depend on it
// for clarity even though sharing would work in this package.
func freeIntegrationPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// destroyIntegrationSandbox tears down a sandbox via the real
// sandbox.Destroy. Used from t.Cleanup so test exit always releases
// the port and removes the data dir, even when an assertion fails
// partway through.
func destroyIntegrationSandbox(t *testing.T, runner pgexec.Runner, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Destroy(ctx, runner, DestroyOptions{SandboxDir: dir}, os.Stderr); err != nil {
		t.Logf("cleanup Destroy(%s): %v", dir, err)
	}
}

func TestIntegrationDeploy_HappyPath(t *testing.T) {
	binDir := skipUnlessRealPG(t)
	runner := pgexec.New(binDir)

	sandboxDir := filepath.Join(t.TempDir(), "sb")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir: sandboxDir,
		BinDir:     binDir,
		// Port = 0 with PortExplicit = false exercises portalloc's
		// auto-allocation against a real-PG environment.
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// Ensure cleanup runs even if a later assertion fatals.
	t.Cleanup(func() { destroyIntegrationSandbox(t, runner, sandboxDir) })

	if res == nil || res.Sandbox == nil {
		t.Fatal("Deploy: result or Sandbox missing")
	}
	if res.Sandbox.Port <= 0 {
		t.Errorf("Sandbox.Port: got %d, want > 0", res.Sandbox.Port)
	}
	if !config.IsSandboxDir(sandboxDir) {
		t.Errorf("after Deploy, IsSandboxDir(%q) = false", sandboxDir)
	}

	// Probe the live server with psql -c 'SELECT 1' to confirm the
	// deploy actually produced a usable instance, not just a happy
	// return code.
	psqlCtx, psqlCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer psqlCancel()
	probe := runner.Run(psqlCtx, "psql",
		"-X", "-A", "-t",
		"-h", res.Sandbox.Host,
		"-p", strconv.Itoa(res.Sandbox.Port),
		"-U", res.Sandbox.Superuser,
		"-d", res.Sandbox.DefaultDatabase,
		"-c", "SELECT 1;",
	)
	if probe.Err != nil || probe.ExitCode != 0 {
		t.Fatalf("psql SELECT 1: exit=%d err=%v stderr=%s",
			probe.ExitCode, probe.Err, string(probe.Stderr))
	}
}
