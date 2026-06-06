// Tests for the logical-replication slice: Publish, Subscribe,
// Deploy --subscribe-to, destroy-side DROP SUBSCRIPTION, and the
// publication/subscription extensions in Status.
//
// These tests use pgexec.Fake and t.TempDir(); none of them launch
// a real PostgreSQL. As with replication_test.go, source-side calls
// reuse the same Fake the destination uses — so a single FakeCalls
// slice records all argv we want to assert on.
//
// A subtle point on Fake's response model: every call to a given
// binary returns the SAME canned Result. For multi-step flows like
// Publish (SHOW wal_level → ALTER SYSTEM → SHOW max_replication_slots
// → ALTER SYSTEM → Stop → Start → CREATE PUBLICATION) we work around
// this by either (a) injecting the response that satisfies every
// step (e.g. "logical\n" makes all SHOW callers happy AND makes the
// integer parser fail benignly so we skip raising slot counts) or
// (b) iterating f.Calls afterwards and asserting on what argv each
// step received.

package sandbox

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// ---------------------------------------------------------------
// Publish
// ---------------------------------------------------------------

func TestPublishHappyPathNoRestart(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// "logical" satisfies SHOW wal_level; for the integer SHOWs it
	// fails Atoi which means we skip raising those (benign — the
	// happy path is "already configured").
	f.SetResult("psql", pgexec.Result{Stdout: []byte("logical\n"), ExitCode: 0})

	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "my_pub",
		AllTables:  true,
	}, io.Discard)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// No pg_ctl stop/start should have been called (no restart).
	for _, c := range f.Calls {
		if c.Name == "pg_ctl" {
			t.Errorf("unexpected pg_ctl call (no restart should be needed); %+v", c)
		}
	}
	// CREATE PUBLICATION must have appeared in a psql -c argv.
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "CREATE PUBLICATION my_pub FOR ALL TABLES") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("CREATE PUBLICATION not issued; calls=%v", f.Calls)
	}
}

func TestPublishRequiresRunning(t *testing.T) {
	dir := deployFixture(t)
	// No pidfile → not running.
	f := &pgexec.Fake{}
	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "my_pub",
		AllTables:  true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error when not running")
	}
	if ExitCodeFor(err) != ui.ExitPublicationFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitPublicationFailed)
	}
}

func TestPublishMutexAllTablesAndTables(t *testing.T) {
	dir := deployFixture(t)
	f := &pgexec.Fake{}
	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "p",
		AllTables:  true,
		Tables:     []string{"t1"},
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitUsage")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestPublishNeitherAllTablesNorTables(t *testing.T) {
	dir := deployFixture(t)
	f := &pgexec.Fake{}
	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "p",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitUsage")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestPublishMissingPubName(t *testing.T) {
	dir := deployFixture(t)
	f := &pgexec.Fake{}
	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		AllTables:  true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitUsage")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestPublishNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: tmp,
		PubName:    "p",
		AllTables:  true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitNotASandbox")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestPublishCreatePublicationFails(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// Every psql call returns "logical" stdout + exit 1 (so the
	// CREATE PUBLICATION fails — and the SHOW wal_level happy path
	// is also fine because TrimSpace("logical") == "logical").
	// Actually, we need SHOW to succeed but CREATE to fail. The Fake
	// gives one response per binary; we set exit 1 so EVERY psql
	// call gets exit 1, including the SHOW. We assert on
	// ExitPublicationFailed regardless of which step tripped.
	f.SetResult("psql", pgexec.Result{
		Stdout:   []byte("logical\n"),
		Stderr:   []byte("ERROR:  publication \"my_pub\" already exists\n"),
		ExitCode: 1,
	})
	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "my_pub",
		AllTables:  true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitPublicationFailed")
	}
	if ExitCodeFor(err) != ui.ExitPublicationFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitPublicationFailed)
	}
}

func TestPublishRestartsWhenWalLevelWrong(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// "replica" is the initdb default; this triggers ALTER SYSTEM +
	// restart. The same canned response is returned for SHOW
	// max_replication_slots (parsed as int fails → skip raising) and
	// for ALTER SYSTEM (exit 0 → ALTER succeeds). CREATE PUBLICATION
	// also gets this response (exit 0), so the flow completes.
	f.SetResult("psql", pgexec.Result{Stdout: []byte("replica\n"), ExitCode: 0})
	// pg_ctl stop/start must succeed.
	f.SetResult("pg_ctl", pgexec.Result{ExitCode: 0})

	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "my_pub",
		AllTables:  true,
	}, io.Discard)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// pg_ctl stop must have been called (the restart). We do NOT
	// assert pg_ctl start: our Start() short-circuits when the
	// pidfile is still present, and the Fake doesn't remove it
	// during Stop. The intent of the assertion is "we called the
	// restart path"; pg_ctl stop alone suffices to prove that.
	foundStop := false
	for _, c := range f.Calls {
		if c.Name != "pg_ctl" {
			continue
		}
		if len(c.Args) > 0 && c.Args[0] == "stop" {
			foundStop = true
		}
	}
	if !foundStop {
		t.Errorf("expected pg_ctl stop during restart; calls=%v", f.Calls)
	}
	// ALTER SYSTEM SET wal_level should appear in a psql -c argv.
	foundAlter := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "ALTER SYSTEM SET wal_level") {
				foundAlter = true
			}
		}
	}
	if !foundAlter {
		t.Errorf("ALTER SYSTEM SET wal_level not issued; calls=%v", f.Calls)
	}
}

func TestPublishWithExplicitTables(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	f.SetResult("psql", pgexec.Result{Stdout: []byte("logical\n"), ExitCode: 0})

	err := Publish(context.Background(), f, PublishOptions{
		SandboxDir: dir,
		PubName:    "my_pub",
		Tables:     []string{"public.t1", "public.t2"},
	}, io.Discard)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "CREATE PUBLICATION my_pub FOR TABLE public.t1, public.t2") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("CREATE PUBLICATION FOR TABLE not issued; calls=%v", f.Calls)
	}
}

// ---------------------------------------------------------------
// Subscribe
// ---------------------------------------------------------------

func TestSubscribeHappyPath(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	pubPort := freeProbePort(t)
	makeRunningSourceFixture(t, pubDir, "pub", binDir, pubPort)

	// Subscriber sandbox: a deployed, running fixture distinct from
	// the publisher.
	subDir := filepath.Join(root, "sub1")
	subPort := freeProbePort(t)
	mustWriteSandboxFile(t, subDir, "sub1", binDir, subPort)
	subCfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, subCfg.DataDir)

	f := &pgexec.Fake{}
	err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "pub",
		PubName:      "my_pub",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// CREATE SUBSCRIPTION must have appeared.
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "CREATE SUBSCRIPTION") &&
				strings.Contains(a, "PUBLICATION my_pub") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("CREATE SUBSCRIPTION not issued; calls=%v", f.Calls)
	}

	// Config must now record subscription metadata.
	cfg2, err := config.LoadSandbox(subDir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.Role != config.RoleSubscriber {
		t.Errorf("role: got %q, want subscriber", cfg2.Role)
	}
	if cfg2.Logical == nil {
		t.Fatal("Logical block missing after Subscribe")
	}
	if cfg2.Logical.SubscriptionName != "sub1_sub" {
		t.Errorf("default sub name: got %q, want sub1_sub", cfg2.Logical.SubscriptionName)
	}
	if cfg2.Logical.PublicationName != "my_pub" {
		t.Errorf("pub name: got %q", cfg2.Logical.PublicationName)
	}
	if cfg2.Logical.SourceSandbox != "pub" {
		t.Errorf("source: got %q", cfg2.Logical.SourceSandbox)
	}
	if cfg2.Logical.CopyMode != config.CopyAll {
		t.Errorf("copy mode: got %q, want all", cfg2.Logical.CopyMode)
	}
}

func TestSubscribeNoCopyData(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	makeRunningSourceFixture(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	mustWriteSandboxFile(t, subDir, "sub1", binDir, freeProbePort(t))
	subCfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, subCfg.DataDir)

	f := &pgexec.Fake{}
	if err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "pub",
		PubName:      "my_pub",
		NoCopyData:   true,
	}, io.Discard); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// CREATE SUBSCRIPTION must include copy_data = false.
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "copy_data = false") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected copy_data = false; calls=%v", f.Calls)
	}
	cfg2, _ := config.LoadSandbox(subDir)
	if cfg2.Logical.CopyMode != config.CopyNone {
		t.Errorf("copy mode: got %q, want none", cfg2.Logical.CopyMode)
	}
}

func TestSubscribeCopySchemaRunsPgDumpAndPsql(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	makeRunningSourceFixture(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	mustWriteSandboxFile(t, subDir, "sub1", binDir, freeProbePort(t))
	subCfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, subCfg.DataDir)

	f := &pgexec.Fake{}
	// pg_dump returns some SQL on stdout — the Fake's
	// RunWithStdin will then receive it as the apply step's stdin.
	f.SetResult("pg_dump", pgexec.Result{
		Stdout:   []byte("CREATE TABLE t (id int);\n"),
		ExitCode: 0,
	})
	if err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "pub",
		PubName:      "my_pub",
		CopySchema:   true,
	}, io.Discard); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// pg_dump must have been called with --schema-only.
	if !callsContainArg(f, "pg_dump", "--schema-only") {
		t.Errorf("pg_dump --schema-only not called; calls=%v", f.Calls)
	}
	// RunWithStdin must have been used to apply the schema (psql).
	foundStdin := false
	for _, c := range f.Calls {
		if c.Method == "RunWithStdin" && c.Name == "psql" {
			foundStdin = true
			if !strings.Contains(string(c.Stdin), "CREATE TABLE t") {
				t.Errorf("stdin did not include dumped SQL; got: %q", c.Stdin)
			}
		}
	}
	if !foundStdin {
		t.Errorf("expected RunWithStdin call for psql apply; calls=%v", f.Calls)
	}
	cfg2, _ := config.LoadSandbox(subDir)
	if cfg2.Logical.CopyMode != config.CopySchema {
		t.Errorf("copy mode: got %q, want schema", cfg2.Logical.CopyMode)
	}
}

func TestSubscribePublisherNotRunning(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	// Publisher exists on disk but is NOT running (no pidfile, no listener).
	mustWriteSandboxFile(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	mustWriteSandboxFile(t, subDir, "sub1", binDir, freeProbePort(t))
	subCfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, subCfg.DataDir)

	f := &pgexec.Fake{}
	err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "pub",
		PubName:      "my_pub",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitSourceUnreachable")
	}
	if ExitCodeFor(err) != ui.ExitSourceUnreachable {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitSourceUnreachable)
	}
}

func TestSubscribeCreateFails(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	makeRunningSourceFixture(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	mustWriteSandboxFile(t, subDir, "sub1", binDir, freeProbePort(t))
	subCfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, subCfg.DataDir)

	f := &pgexec.Fake{}
	f.SetResult("psql", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("ERROR:  could not connect to publisher\n"),
	})
	err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "pub",
		PubName:      "my_pub",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitSubscriptionFailed")
	}
	if ExitCodeFor(err) != ui.ExitSubscriptionFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitSubscriptionFailed)
	}
}

func TestSubscribeSchemaCopyFails(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	makeRunningSourceFixture(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	mustWriteSandboxFile(t, subDir, "sub1", binDir, freeProbePort(t))
	subCfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, subCfg.DataDir)

	f := &pgexec.Fake{}
	f.SetResult("pg_dump", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("pg_dump: error: connection refused\n"),
	})
	err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   subDir,
		PublisherRef: "pub",
		PubName:      "my_pub",
		CopySchema:   true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitSchemaCopyFailed")
	}
	if ExitCodeFor(err) != ui.ExitSchemaCopyFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitSchemaCopyFailed)
	}
}

func TestSubscribeNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	err := Subscribe(context.Background(), f, SubscribeOptions{
		SandboxDir:   tmp,
		PublisherRef: "pub",
		PubName:      "p",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitNotASandbox")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestSubscribeMissingRequiredFields(t *testing.T) {
	f := &pgexec.Fake{}
	cases := []SubscribeOptions{
		{SandboxDir: "", PublisherRef: "p", PubName: "x"},
		{SandboxDir: "/tmp/sb", PublisherRef: "", PubName: "x"},
		{SandboxDir: "/tmp/sb", PublisherRef: "p", PubName: ""},
	}
	for i, tc := range cases {
		err := Subscribe(context.Background(), f, tc, io.Discard)
		if err == nil {
			t.Errorf("case[%d]: expected error", i)
			continue
		}
		if ExitCodeFor(err) != ui.ExitUsage {
			t.Errorf("case[%d]: exit code: got %d, want %d", i, ExitCodeFor(err), ui.ExitUsage)
		}
	}
}

// ---------------------------------------------------------------
// Deploy --subscribe-to
// ---------------------------------------------------------------

func TestDeploySubscriberHappyPath(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	makeRunningSourceFixture(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	f := &pgexec.Fake{}
	res, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   subDir,
		BinDir:       binDir,
		Port:         freeProbePort(t),
		PortExplicit: true,
		SubscribeTo:  "pub",
		PubName:      "my_pub",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Deploy --subscribe-to: %v", err)
	}
	if res == nil || res.Sandbox == nil {
		t.Fatal("Deploy result missing")
	}
	if res.Sandbox.Role != config.RoleSubscriber {
		t.Errorf("role: got %q, want subscriber", res.Sandbox.Role)
	}
	if res.Sandbox.Logical == nil {
		t.Fatal("Logical block missing")
	}
	if res.Sandbox.Logical.PublicationName != "my_pub" {
		t.Errorf("pub name: got %q", res.Sandbox.Logical.PublicationName)
	}
	// initdb must have been called (the standalone part).
	if !callsContain(f, "initdb", "") {
		// callsContain matches first arg; initdb has no subcmd —
		// we walk Calls manually.
		seen := false
		for _, c := range f.Calls {
			if c.Name == "initdb" {
				seen = true
			}
		}
		if !seen {
			t.Errorf("initdb not called; calls=%v", f.Calls)
		}
	}
	// CREATE SUBSCRIPTION must have been issued.
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "CREATE SUBSCRIPTION") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("CREATE SUBSCRIPTION not issued; calls=%v", f.Calls)
	}
}

func TestDeploySubscriberRequiresPubName(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pubDir := filepath.Join(root, "pub")
	makeRunningSourceFixture(t, pubDir, "pub", binDir, freeProbePort(t))

	subDir := filepath.Join(root, "sub1")
	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   subDir,
		BinDir:       binDir,
		Port:         freeProbePort(t),
		PortExplicit: true,
		SubscribeTo:  "pub",
		// PubName intentionally omitted.
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitUsage")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestDeployRefusesReplicateAndSubscribe(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "sub1")
	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:    subDir,
		BinDir:        binDir,
		Port:          freeProbePort(t),
		PortExplicit:  true,
		ReplicateFrom: "primary",
		SlotName:      "slot1",
		SubscribeTo:   "pub",
		PubName:       "p",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected ExitUsage")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

// ---------------------------------------------------------------
// Destroy DROP SUBSCRIPTION
// ---------------------------------------------------------------

func TestDestroyDropsSubscription(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "sub1")
	mustWriteSubscriberSandbox(t, subDir, "sub1", binDir, freeProbePort(t),
		"pub", "my_pub", "sub1_sub")
	cfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	var stderr bytes.Buffer
	if err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: subDir}, &stderr); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// A psql call with DROP SUBSCRIPTION must have been made.
	found := false
	for _, c := range f.Calls {
		if c.Name != "psql" {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, "DROP SUBSCRIPTION sub1_sub") &&
				strings.Contains(a, "slot_name = NONE") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected DROP SUBSCRIPTION with slot_name=NONE; calls=%v", f.Calls)
	}
}

func TestDestroySubscriptionFailureIsBestEffort(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "sub1")
	mustWriteSubscriberSandbox(t, subDir, "sub1", binDir, freeProbePort(t),
		"pub", "my_pub", "sub1_sub")
	cfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	f.SetResult("psql", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("ERROR:  could not connect to publisher\n"),
	})
	// Destroy should still proceed despite psql failure.
	if err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: subDir}, io.Discard); err != nil {
		t.Fatalf("Destroy should be best-effort: %v", err)
	}
	if _, err := os.Stat(subDir); err == nil {
		t.Errorf("sandbox dir should be gone after destroy")
	}
}

// ---------------------------------------------------------------
// Status: publications + subscription
// ---------------------------------------------------------------

func TestStatusReportsPublications(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// Every psql call returns "my_pub\nother_pub" — the version
	// probe trims to first line, so Version becomes "my_pub" (junk
	// but acceptable for this test); the pg_publication probe parses
	// both lines as names.
	f.SetResult("psql", pgexec.Result{
		Stdout:   []byte("my_pub\nother_pub\n"),
		ExitCode: 0,
	})
	rep, err := Status(context.Background(), f, dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Publications == nil {
		t.Fatal("Publications nil; expected populated list")
	}
	if len(rep.Publications) != 2 {
		t.Errorf("expected 2 publications, got %d: %v", len(rep.Publications), rep.Publications)
	}
	// RenderText should mention the publications line.
	var buf bytes.Buffer
	rep.RenderText(&buf)
	if !strings.Contains(buf.String(), "publications=[my_pub, other_pub]") {
		t.Errorf("RenderText missing publications line; got: %s", buf.String())
	}
}

func TestStatusReportsSubscription(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "sub1")
	mustWriteSubscriberSandbox(t, subDir, "sub1", binDir, freeProbePort(t),
		"pub", "my_pub", "sub1_sub")
	cfg, _ := config.LoadSandbox(subDir)
	mustCreatePid(t, cfg.DataDir)
	ln := mustListenOn(t, cfg.Host, cfg.Port)
	defer ln.Close()

	f := &pgexec.Fake{}
	// Same Fake response across all psql calls. Pipe-delimited row
	// shape: subname|enabled|pid|received_lsn|latest_end_lsn|last_msg_send_time.
	f.SetResult("psql", pgexec.Result{
		Stdout:   []byte("sub1_sub|t|12345|0/16B7E80|0/16B7E80|2024-01-01 00:00:00+00\n"),
		ExitCode: 0,
	})
	rep, err := Status(context.Background(), f, subDir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Subscription == nil {
		t.Fatal("Subscription nil; expected populated row")
	}
	if rep.Subscription.Name != "sub1_sub" {
		t.Errorf("Subscription.Name: got %q", rep.Subscription.Name)
	}
	if !rep.Subscription.Enabled {
		t.Errorf("Subscription.Enabled: got false, want true")
	}
	if rep.Subscription.WorkerPID != "12345" {
		t.Errorf("Subscription.WorkerPID: got %q", rep.Subscription.WorkerPID)
	}
	// RenderText should include the subscription line.
	var buf bytes.Buffer
	rep.RenderText(&buf)
	if !strings.Contains(buf.String(), "subscription=name=sub1_sub enabled=true") {
		t.Errorf("RenderText missing subscription line; got: %s", buf.String())
	}
}

// ---------------------------------------------------------------
// Helpers specific to logical tests
// ---------------------------------------------------------------

// mustWriteSubscriberSandbox writes a config-valid subscriber sandbox.
func mustWriteSubscriberSandbox(t *testing.T, dir, name, binDir string, port int, sourceName, pubName, subName string) {
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
	cfg.Role = config.RoleSubscriber
	cfg.Logical = &config.Logical{
		SourceSandbox:    sourceName,
		PublicationName:  pubName,
		SubscriptionName: subName,
		CopyMode:         config.CopyAll,
		TargetDatabase:   cfg.DefaultDatabase,
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := config.SaveSandbox(dir, &cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
}
