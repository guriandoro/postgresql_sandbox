// Unit tests for `use` and `run` argv/env construction.
//
// These tests exercise PrepareUse and PrepareRun directly — no
// runner.Exec gymnastics needed because both functions return a
// pure data struct (UseInvocation / RunInvocation). The CLI layer
// is what actually hands those to runner.Exec; that wiring is
// covered by the integration smoke test, not here.
//
// LocateUseBinary and LocateRunBinary ARE exercised through a
// Fake runner so we can prove that a missing binary surfaces as a
// readable error (and so a Fake with LocateErr set produces the
// expected propagation).

package sandbox

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// ---------------------------------------------------------------
// PrepareUse
// ---------------------------------------------------------------

func TestPrepareUseHappyPath(t *testing.T) {
	dir := deployFixture(t)
	cfg, err := config.LoadSandbox(dir)
	if err != nil {
		t.Fatalf("LoadSandbox: %v", err)
	}

	extra := []string{"-c", "SELECT 1", "-A", "-t"}
	inv, err := PrepareUse(context.Background(), dir, extra)
	if err != nil {
		t.Fatalf("PrepareUse: %v", err)
	}
	if inv.Binary != "psql" {
		t.Errorf("Binary: got %q, want %q", inv.Binary, "psql")
	}

	// The first 8 args must be -h/-p/-U/-d with the sandbox's
	// resolved values; the extras follow verbatim.
	wantPrefix := []string{
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
	}
	if len(inv.Args) != len(wantPrefix)+len(extra) {
		t.Fatalf("Args length: got %d (%v), want %d", len(inv.Args), inv.Args, len(wantPrefix)+len(extra))
	}
	for i, w := range wantPrefix {
		if inv.Args[i] != w {
			t.Errorf("Args[%d]: got %q, want %q (full: %v)", i, inv.Args[i], w, inv.Args)
		}
	}
	for i, w := range extra {
		if inv.Args[len(wantPrefix)+i] != w {
			t.Errorf("Args[%d] (extra): got %q, want %q", len(wantPrefix)+i, inv.Args[len(wantPrefix)+i], w)
		}
	}

	// Env must include all four PG* keys with the right values.
	wantEnv := map[string]string{
		"PGHOST":     cfg.Host,
		"PGPORT":     strconv.Itoa(cfg.Port),
		"PGUSER":     cfg.Superuser,
		"PGDATABASE": cfg.DefaultDatabase,
	}
	gotEnv := envToMap(inv.Env)
	for k, want := range wantEnv {
		if got, ok := gotEnv[k]; !ok || got != want {
			t.Errorf("Env[%s]: got %q (present=%v), want %q", k, got, ok, want)
		}
	}
}

func TestPrepareUseNoExtras(t *testing.T) {
	dir := deployFixture(t)
	inv, err := PrepareUse(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("PrepareUse: %v", err)
	}
	// Only the four flag pairs.
	if len(inv.Args) != 8 {
		t.Errorf("Args length: got %d, want 8", len(inv.Args))
	}
}

func TestPrepareUseNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	_, err := PrepareUse(context.Background(), tmp, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestPrepareUseEmptyDir(t *testing.T) {
	_, err := PrepareUse(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestLocateUseBinaryFakeMissing(t *testing.T) {
	f := &pgexec.Fake{LocateErr: errors.New("psql: command not found")}
	_, err := LocateUseBinary(f)
	if err == nil {
		t.Fatal("expected locate error")
	}
}

func TestLocateUseBinaryFakeOK(t *testing.T) {
	f := &pgexec.Fake{}
	path, err := LocateUseBinary(f)
	if err != nil {
		t.Fatalf("LocateUseBinary: %v", err)
	}
	if path != "/fake/psql" {
		t.Errorf("path: got %q, want /fake/psql", path)
	}
}

// ---------------------------------------------------------------
// PrepareRun
// ---------------------------------------------------------------

func TestPrepareRunHappyPath(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)

	forwarded := []string{"-F", "c", "-t", "mytable", "mydb_arg"}
	inv, err := PrepareRun(context.Background(), RunOptions{
		SandboxDir: dir,
		Binary:     "pg_dump",
		ExtraArgs:  forwarded,
	})
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}

	if inv.Binary != "pg_dump" {
		t.Errorf("Binary: got %q, want pg_dump", inv.Binary)
	}

	// Without --no-dsn, argv begins with -h/-p/-U/-d. Since
	// forwarded has no -d, the heuristic should inject -d.
	wantPrefix := []string{
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
	}
	if len(inv.Args) != len(wantPrefix)+len(forwarded) {
		t.Fatalf("Args length: got %d (%v)", len(inv.Args), inv.Args)
	}
	for i, w := range wantPrefix {
		if inv.Args[i] != w {
			t.Errorf("Args[%d]: got %q, want %q", i, inv.Args[i], w)
		}
	}
}

func TestPrepareRunNoDSN(t *testing.T) {
	dir := deployFixture(t)
	forwarded := []string{"-F", "c", "mydb"}

	inv, err := PrepareRun(context.Background(), RunOptions{
		SandboxDir: dir,
		Binary:     "pg_dump",
		ExtraArgs:  forwarded,
		NoDSN:      true,
	})
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}

	// --no-dsn: argv is the forwarded args untouched. No
	// -h/-p/-U/-d injected at all.
	if len(inv.Args) != len(forwarded) {
		t.Fatalf("Args length: got %d, want %d (no-dsn should not prepend)", len(inv.Args), len(forwarded))
	}
	for i, w := range forwarded {
		if inv.Args[i] != w {
			t.Errorf("Args[%d]: got %q, want %q", i, inv.Args[i], w)
		}
	}

	// Env MUST still be set — that's the SPEC §6.6 contract.
	gotEnv := envToMap(inv.Env)
	for _, k := range []string{"PGHOST", "PGPORT", "PGUSER", "PGDATABASE"} {
		if _, ok := gotEnv[k]; !ok {
			t.Errorf("Env missing %s with --no-dsn (env must still be set)", k)
		}
	}
}

func TestPrepareRunDbnameHeuristic(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)

	// Each case lists the forwarded args; the assertion is "did
	// we inject -d <defaultDatabase>?".
	cases := []struct {
		name        string
		forwarded   []string
		wantInjectD bool
	}{
		{"no dbname", []string{"-t", "mytable"}, true},
		{"-d explicit", []string{"-d", "otherdb", "-t", "mytable"}, false},
		{"-dfoo libpq short", []string{"-dmydb"}, false},
		{"--dbname long", []string{"--dbname", "otherdb"}, false},
		{"--dbname= equals", []string{"--dbname=otherdb"}, false},
		{"dbname= positional", []string{"dbname=otherdb"}, false},
		{"--port has no d", []string{"--port", "5432"}, true},
		{"port=5432", []string{"port=5432"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := PrepareRun(context.Background(), RunOptions{
				SandboxDir: dir,
				Binary:     "pgbench",
				ExtraArgs:  tc.forwarded,
			})
			if err != nil {
				t.Fatalf("PrepareRun: %v", err)
			}
			// Look for "-d" "<defaultDatabase>" pair appearing
			// BEFORE the forwarded args (i.e. injected by us, not
			// from the forwarded slice).
			injected := false
			// We injected if -d appears in the first 8 positions
			// AND we built 8 prefix elements. With heuristic
			// triggered, prefix is 6 elements (no -d).
			if len(inv.Args) >= 8 && inv.Args[6] == "-d" && inv.Args[7] == cfg.DefaultDatabase {
				// Verify it's our injection (the position
				// matches): the forwarded args start at index 8
				// when we injected, at index 6 when we didn't.
				if len(inv.Args)-len(tc.forwarded) == 8 {
					injected = true
				}
			}
			if injected != tc.wantInjectD {
				t.Errorf("case %q: injected=-d? got %v, want %v (Args=%v)",
					tc.name, injected, tc.wantInjectD, inv.Args)
			}
		})
	}
}

func TestPrepareRunMissingBinary(t *testing.T) {
	dir := deployFixture(t)
	_, err := PrepareRun(context.Background(), RunOptions{
		SandboxDir: dir,
		Binary:     "",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestPrepareRunMissingSandboxDir(t *testing.T) {
	_, err := PrepareRun(context.Background(), RunOptions{
		Binary: "pg_dump",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestPrepareRunNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	_, err := PrepareRun(context.Background(), RunOptions{
		SandboxDir: tmp,
		Binary:     "pg_dump",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestLocateRunBinaryEmpty(t *testing.T) {
	f := &pgexec.Fake{}
	_, err := LocateRunBinary(f, "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestLocateRunBinaryFakeMissing(t *testing.T) {
	f := &pgexec.Fake{LocateErr: errors.New("not found")}
	_, err := LocateRunBinary(f, "pg_dump")
	if err == nil {
		t.Fatal("expected locate error")
	}
}

func TestLocateRunBinaryFakeOK(t *testing.T) {
	f := &pgexec.Fake{}
	path, err := LocateRunBinary(f, "pg_dump")
	if err != nil {
		t.Fatalf("LocateRunBinary: %v", err)
	}
	if path != "/fake/pg_dump" {
		t.Errorf("path: got %q, want /fake/pg_dump", path)
	}
}

// ---------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------

// envToMap splits a KEY=VALUE slice into a map for easy assertion.
// Duplicate keys take the last-write-wins value, matching how
// child process env resolves under exec().
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				m[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return m
}
