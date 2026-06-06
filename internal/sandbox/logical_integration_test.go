//go:build integration

// Integration smoke test for logical pub/sub: deploy a publisher
// sandbox, run sandbox.Publish to create a publication (which also
// flips wal_level=logical and restarts the publisher), deploy a
// subscriber sandbox, run sandbox.Subscribe with CopySchema so the
// subscriber has somewhere for copy_data to land, then verify an
// insert on the publisher arrives at the subscriber within a bounded
// poll window.

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

func TestIntegrationLogicalPubSub(t *testing.T) {
	binDir := skipUnlessRealPG(t)
	runner := pgexec.New(binDir)

	root := t.TempDir()
	pubDir := filepath.Join(root, "publisher")
	subDir := filepath.Join(root, "subscriber")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Step 1: deploy the publisher (standalone). Publish() raises
	// wal_level via ALTER SYSTEM + restart, so the deploy itself
	// doesn't need to know about logical replication.
	pubRes, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir: pubDir,
		BinDir:     binDir,
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Deploy publisher: %v", err)
	}
	t.Cleanup(func() { destroyIntegrationSandbox(t, runner, pubDir) })

	// Step 2: seed a table on the publisher BEFORE Publish so the
	// publication's FOR ALL TABLES picks it up. We could also
	// CREATE the table after publish — FOR ALL TABLES catches new
	// tables too — but seeding first lets us cover the copy_data
	// path on the subscribe side.
	const (
		ddl     = "CREATE TABLE smoke_logical (id int PRIMARY KEY, payload text); INSERT INTO smoke_logical VALUES (1, 'seed');"
		insert2 = "INSERT INTO smoke_logical VALUES (2, 'live');"
		query   = "SELECT count(*) FROM smoke_logical WHERE id IN (1, 2);"
		expect  = "2"
	)
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", pubRes.Sandbox.Host,
		"-p", strconv.Itoa(pubRes.Sandbox.Port),
		"-U", pubRes.Sandbox.Superuser,
		"-d", pubRes.Sandbox.DefaultDatabase,
		"-c", ddl,
	)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("publisher seed DDL: exit=%d err=%v stderr=%s",
			res.ExitCode, res.Err, string(res.Stderr))
	}

	// Step 3: run Publish. This flips wal_level + restarts.
	if err := Publish(ctx, runner, PublishOptions{
		SandboxDir: pubDir,
		PubName:    "smoke_pub",
		AllTables:  true,
	}, os.Stderr); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Step 4: deploy the subscriber.
	if _, err := Deploy(ctx, runner, DeployOptions{
		SandboxDir: subDir,
		BinDir:     binDir,
	}, os.Stderr); err != nil {
		t.Fatalf("Deploy subscriber: %v", err)
	}
	t.Cleanup(func() { destroyIntegrationSandbox(t, runner, subDir) })

	// Step 5: subscribe. CopySchema=true so the subscriber gets the
	// table definition pulled across before copy_data lands rows.
	if err := Subscribe(ctx, runner, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "publisher",
		PubName:      "smoke_pub",
		CopySchema:   true,
	}, os.Stderr); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Step 6: insert a second row on the publisher to exercise the
	// live (non-copy_data) replication path.
	res = runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", pubRes.Sandbox.Host,
		"-p", strconv.Itoa(pubRes.Sandbox.Port),
		"-U", pubRes.Sandbox.Superuser,
		"-d", pubRes.Sandbox.DefaultDatabase,
		"-c", insert2,
	)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("publisher insert2: exit=%d err=%v stderr=%s",
			res.ExitCode, res.Err, string(res.Stderr))
	}

	// Step 7: poll the subscriber until both rows are visible. ~10s
	// covers logical-replication startup + apply latency on slow CI
	// without letting a wedged test hang forever.
	subCfg, err := loadSandboxOrFail(subDir)
	if err != nil {
		t.Fatalf("loadSandboxOrFail(subscriber): %v", err)
	}
	if !pollPsqlEquals(runner, subCfg, query, expect, 10*time.Second) {
		t.Fatalf("subscriber did not see both rows within 10s")
	}
}
