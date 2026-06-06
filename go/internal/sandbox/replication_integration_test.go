//go:build integration

// Integration smoke test for physical streaming replication: deploy
// a primary, deploy a standby via --replicate-from, verify that
// pg_is_in_recovery() is true on the standby, then write a tiny
// table on the primary and confirm it shows up on the standby
// within a bounded backoff window.

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
)

func TestIntegrationPhysicalReplication(t *testing.T) {
	binDir := skipUnlessRealPG(t)
	runner := pgexec.New(binDir)

	// Sibling dirs under one tmp root so the standby can reference
	// the primary by bare name (resolveSourceSandbox handles this).
	root := t.TempDir()
	primaryDir := filepath.Join(root, "primary")
	standbyDir := filepath.Join(root, "standby1")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Step 1: deploy the primary.
	primaryRes, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir: primaryDir,
		BinDir:     binDir,
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Deploy primary: %v", err)
	}
	// t.Cleanup is LIFO: register primary cleanup first so the
	// standby's cleanup (registered next) runs BEFORE the primary's,
	// letting the standby's best-effort slot drop reach a live
	// upstream.
	t.Cleanup(func() { destroyIntegrationSandbox(t, runner, primaryDir) })

	// Step 2: deploy the standby pointing at the primary.
	standbyRes, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir:    standbyDir,
		BinDir:        binDir,
		ReplicateFrom: "primary",
		SlotName:      "smoke_standby_slot",
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Deploy standby: %v", err)
	}
	t.Cleanup(func() { destroyIntegrationSandbox(t, runner, standbyDir) })

	// Step 3: confirm pg_is_in_recovery() on the standby. Use a
	// short bounded retry: the standby may need a moment to finish
	// reaching consistent recovery state after pg_ctl start returns.
	if !pollPsqlEquals(runner, standbyRes.Sandbox, "SELECT pg_is_in_recovery();", "t", 5*time.Second) {
		t.Fatalf("standby pg_is_in_recovery did not become 't' within 5s")
	}

	// Step 4: create a table on the primary, insert a row, and poll
	// the standby until the same row is visible. The bounded backoff
	// guards against streaming-lag flakes on slow CI; 5s is plenty
	// for a single insert in a local sandbox.
	const (
		ddl    = "CREATE TABLE smoke_repl (id int PRIMARY KEY); INSERT INTO smoke_repl VALUES (1);"
		query  = "SELECT count(*) FROM smoke_repl WHERE id = 1;"
		expect = "1"
	)
	exec := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", primaryRes.Sandbox.Host,
		"-p", strconv.Itoa(primaryRes.Sandbox.Port),
		"-U", primaryRes.Sandbox.Superuser,
		"-d", primaryRes.Sandbox.DefaultDatabase,
		"-c", ddl,
	)
	if exec.Err != nil || exec.ExitCode != 0 {
		t.Fatalf("primary DDL: exit=%d err=%v stderr=%s",
			exec.ExitCode, exec.Err, string(exec.Stderr))
	}
	if !pollPsqlEquals(runner, standbyRes.Sandbox, query, expect, 5*time.Second) {
		t.Fatalf("standby did not see replicated row within 5s")
	}
}

// pollPsqlEquals runs a psql query against sb every 100ms until the
// trimmed stdout matches want or max elapses. Returns true on match,
// false on deadline. Uses -X -A -t for deterministic single-line
// output regardless of the host's psqlrc.
//
// Shared by the physical-replication and logical-pubsub tests in
// this package — both need "poll a downstream sandbox until a query
// yields the expected answer" with a bounded backoff.
func pollPsqlEquals(runner pgexec.Runner, cfg *config.Sandbox, query, want string, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res := runner.Run(ctx, "psql",
			"-X", "-A", "-t",
			"-h", cfg.Host,
			"-p", strconv.Itoa(cfg.Port),
			"-U", cfg.Superuser,
			"-d", cfg.DefaultDatabase,
			"-c", query,
		)
		cancel()
		if res.Err == nil && res.ExitCode == 0 {
			if strings.TrimSpace(string(res.Stdout)) == want {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
