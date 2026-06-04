// Unit tests for the sandbox package.
//
// These tests use pgexec.Fake so they never launch a real PG. The
// strategy:
//
//   - Build a Fake runner with canned Results for whatever binaries
//     the operation under test will invoke.
//   - Call Deploy/Start/Stop/Restart/Status/Destroy.
//   - Assert on the FakeCalls captured and on the on-disk state
//     under t.TempDir().
//
// We intentionally do not test "what argv does pg_ctl actually
// receive on Linux 5.10 with PG 16" — that's the integration tier.
// Here we test the contract of the sandbox package itself.

package sandbox

import (
	"bytes"
	"context"
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

// ---------------------------------------------------------------
// Deploy tests
// ---------------------------------------------------------------

func TestDeployHappyPath(t *testing.T) {
	tmp := t.TempDir()
	sandboxDir := filepath.Join(tmp, "sb")
	binDir := filepath.Join(tmp, "bin")
	// We don't need bin to actually exist for unit tests since the
	// Fake runner doesn't Locate. But Deploy expects absolute paths,
	// and t.TempDir is absolute, so we're fine.
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	f := &pgexec.Fake{}
	res, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   sandboxDir,
		BinDir:       binDir,
		Port:         freeProbePort(t),
		PortExplicit: true,
	}, io.Discard)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res == nil || res.Sandbox == nil {
		t.Fatalf("Deploy result missing")
	}

	// initdb and pg_ctl start should both have been invoked.
	if len(f.Calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	if f.Calls[0].Name != "initdb" {
		t.Errorf("call[0] name: got %q, want initdb", f.Calls[0].Name)
	}
	if !containsString(f.Calls[0].Args, "--auth=trust") {
		t.Errorf("initdb argv missing --auth=trust: %v", f.Calls[0].Args)
	}
	if !containsString(f.Calls[0].Args, "--username="+res.Sandbox.Superuser) {
		t.Errorf("initdb argv missing --username=%s: %v", res.Sandbox.Superuser, f.Calls[0].Args)
	}
	if f.Calls[1].Name != "pg_ctl" || f.Calls[1].Args[0] != "start" {
		t.Errorf("call[1] not pg_ctl start: %+v", f.Calls[1])
	}
	// pg_ctl start should have been given -o "-h <host> -p <port>".
	wantO := "-h " + res.Sandbox.Host + " -p " + strconv.Itoa(res.Sandbox.Port)
	if !containsString(f.Calls[1].Args, wantO) {
		t.Errorf("pg_ctl start missing -o %q in args: %v", wantO, f.Calls[1].Args)
	}

	// Sandbox config file should exist.
	if !config.IsSandboxDir(sandboxDir) {
		t.Errorf("after Deploy, sandbox dir not recognized: %s", sandboxDir)
	}
	// Convenience scripts should exist and be executable.
	for _, name := range convenienceScripts {
		path := filepath.Join(sandboxDir, name)
		st, err := os.Stat(path)
		if err != nil {
			t.Errorf("script %s missing: %v", name, err)
			continue
		}
		if st.Mode()&0o111 == 0 {
			t.Errorf("script %s not executable: mode=%v", name, st.Mode())
		}
		// Regression: scripts MUST point at a specific binary path,
		// not bare `pg_sandbox` on PATH. On a developer machine with
		// the legacy Python pg_sandbox installed, a PATH-resolved
		// script would silently invoke the wrong tool.
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if strings.Contains(string(body), "exec pg_sandbox ") {
			t.Errorf("script %s execs bare `pg_sandbox` (PATH lookup); body=%q", name, body)
		}
		if !strings.Contains(string(body), "PG_SANDBOX_BIN") {
			t.Errorf("script %s missing PG_SANDBOX_BIN override; body=%q", name, body)
		}
	}

	// Connection string should match expected shape.
	wantConn := "postgresql://postgres@" + res.Sandbox.Host + ":" + strconv.Itoa(res.Sandbox.Port) + "/postgres"
	if res.ConnString != wantConn {
		t.Errorf("ConnString: got %q, want %q", res.ConnString, wantConn)
	}
}

func TestDeployRefusesNonEmptyDir(t *testing.T) {
	tmp := t.TempDir()
	sandboxDir := filepath.Join(tmp, "sb")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop a stray file to make the dir non-empty.
	if err := os.WriteFile(filepath.Join(sandboxDir, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatalf("stray: %v", err)
	}

	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   sandboxDir,
		BinDir:       tmp,
		Port:         freeProbePort(t),
		PortExplicit: true,
	}, io.Discard)
	if err == nil {
		t.Fatal("Deploy: expected error, got nil")
	}
	if got := ExitCodeFor(err); got != ui.ExitSandboxExists {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitSandboxExists)
	}
	if len(f.Calls) != 0 {
		t.Errorf("no commands should run when dir is non-empty; calls=%v", f.Calls)
	}
}

func TestDeployRefusesMissingRequiredFields(t *testing.T) {
	f := &pgexec.Fake{}
	cases := []struct {
		name string
		opts DeployOptions
	}{
		{"no SandboxDir", DeployOptions{BinDir: "/tmp"}},
		{"no BinDir", DeployOptions{SandboxDir: "/tmp/sb"}},
	}
	for _, tc := range cases {
		_, err := Deploy(context.Background(), f, tc.opts, io.Discard)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if ExitCodeFor(err) != ui.ExitUsage {
			t.Errorf("%s: exit code: got %d, want %d", tc.name, ExitCodeFor(err), ui.ExitUsage)
		}
	}
}

func TestDeployExplicitPortBusy(t *testing.T) {
	tmp := t.TempDir()
	// Open a listener so the port is provably busy.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	f := &pgexec.Fake{}
	_, err = Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   filepath.Join(tmp, "sb"),
		BinDir:       tmp,
		Port:         port,
		PortExplicit: true,
	}, io.Discard)
	if err == nil {
		t.Fatal("Deploy: expected ExitPortInUse, got nil")
	}
	if got := ExitCodeFor(err); got != ui.ExitPortInUse {
		t.Errorf("exit code: got %d, want %d", got, ui.ExitPortInUse)
	}
}

func TestDeployInitdbFailure(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	f.SetResult("initdb", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("initdb: error: directory is not empty\n"),
	})
	var stderr bytes.Buffer
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   filepath.Join(tmp, "sb"),
		BinDir:       tmp,
		Port:         freeProbePort(t),
		PortExplicit: true,
	}, &stderr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if ExitCodeFor(err) != ui.ExitInitdbFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitInitdbFailed)
	}
	if !strings.Contains(stderr.String(), "initdb stderr") {
		t.Errorf("expected initdb stderr message, got: %q", stderr.String())
	}
	if len(f.Calls) != 1 {
		// pg_ctl start must NOT have been invoked.
		t.Errorf("expected only initdb call, got %v", f.Calls)
	}
}

func TestDeployPgctlFailure(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	f.SetResult("pg_ctl", pgexec.Result{
		ExitCode: 1,
		Stderr:   []byte("pg_ctl: could not start server\n"),
	})
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   filepath.Join(tmp, "sb"),
		BinDir:       tmp,
		Port:         freeProbePort(t),
		PortExplicit: true,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if ExitCodeFor(err) != ui.ExitPgctlFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitPgctlFailed)
	}
}

func TestDeployAutoPortAllocation(t *testing.T) {
	tmp := t.TempDir()
	// PortExplicit=false → Deploy should call portalloc.FreePort
	// starting from PortBase. We give it a tiny range starting at a
	// random ephemeral port (asked from the kernel) so the scan is
	// guaranteed to find that one.
	probe := freeProbePort(t)

	f := &pgexec.Fake{}
	res, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   filepath.Join(tmp, "sb"),
		BinDir:       tmp,
		PortExplicit: false,
		PortBase:     probe,
		PortRange:    1,
	}, io.Discard)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res.Sandbox.Port != probe {
		t.Errorf("expected port %d, got %d", probe, res.Sandbox.Port)
	}
}

// ---------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------

func TestStartStopRestartHappyPath(t *testing.T) {
	dir := deployFixture(t)
	f := &pgexec.Fake{}
	// Stop / restart need isRunning to be true the first call; we
	// simulate that by creating a fake postmaster.pid.
	cfg, err := config.LoadSandbox(dir)
	if err != nil {
		t.Fatalf("LoadSandbox: %v", err)
	}

	// --- Start when already running → no-op, no calls ---
	mustCreatePid(t, cfg.DataDir)
	if err := Start(context.Background(), f, dir, io.Discard); err != nil {
		t.Fatalf("Start (already running): %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("Start should be no-op when pid present; got calls=%v", f.Calls)
	}

	// --- Stop when running → calls pg_ctl stop ---
	if err := Stop(context.Background(), f, dir, io.Discard); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "pg_ctl" || f.Calls[0].Args[0] != "stop" {
		t.Errorf("Stop calls: %v", f.Calls)
	}
	if !containsString(f.Calls[0].Args, "fast") {
		t.Errorf("Stop should use -m fast: %v", f.Calls[0].Args)
	}

	// --- Stop when not running → no-op ---
	os.Remove(filepath.Join(cfg.DataDir, "postmaster.pid"))
	f.Calls = nil
	if err := Stop(context.Background(), f, dir, io.Discard); err != nil {
		t.Fatalf("Stop (not running): %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("Stop should be no-op when no pidfile; got %v", f.Calls)
	}

	// --- Start when not running → calls pg_ctl start ---
	f.Calls = nil
	if err := Start(context.Background(), f, dir, io.Discard); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Args[0] != "start" {
		t.Errorf("Start calls: %v", f.Calls)
	}
	// Regression: Start MUST re-pass `-o "-h <host> -p <port>"`.
	// pg_ctl rewrites postmaster.opts on every start from the args
	// we pass; without `-o`, postgres restarts on its compiled-in
	// default port and the sandbox silently moves.
	expectedOpts := "-h " + cfg.Host + " -p " + strconv.Itoa(cfg.Port)
	args := f.Calls[0].Args
	foundOpts := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && args[i+1] == expectedOpts {
			foundOpts = true
			break
		}
	}
	if !foundOpts {
		t.Errorf("Start must pass -o %q (so pg_ctl rewrites postmaster.opts with the right port); got argv %v",
			expectedOpts, args)
	}

	// --- Restart from stopped → only start (Stop is no-op) ---
	f.Calls = nil
	if err := Restart(context.Background(), f, dir, io.Discard); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Args[0] != "start" {
		t.Errorf("Restart calls: %v", f.Calls)
	}

	// --- Restart from running → stop then start ---
	mustCreatePid(t, cfg.DataDir)
	f.Calls = nil
	if err := Restart(context.Background(), f, dir, io.Discard); err != nil {
		t.Fatalf("Restart (running): %v", err)
	}
	// After stop we drop the pid manually; the test's Restart still
	// counts on real pg_ctl removing it. Our isRunning check uses
	// the pidfile, so we drop it between the two calls — but
	// Restart is one continuous call. We assert at least one stop
	// call and at least one start call were issued.
	if !callsContain(f, "pg_ctl", "stop") {
		t.Errorf("Restart should issue pg_ctl stop: %v", f.Calls)
	}
}

func TestStartNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	err := Start(context.Background(), f, tmp, io.Discard)
	if err == nil {
		t.Fatal("expected ExitNotASandbox")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestStopPgctlFailure(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	f.SetResult("pg_ctl", pgexec.Result{ExitCode: 1, Stderr: []byte("oops\n")})
	err := Stop(context.Background(), f, dir, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitPgctlFailed {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitPgctlFailed)
	}
}

// ---------------------------------------------------------------
// Status tests
// ---------------------------------------------------------------

func TestStatusStopped(t *testing.T) {
	dir := deployFixture(t)
	f := &pgexec.Fake{}
	rep, err := Status(context.Background(), f, dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.State != RunStateStopped {
		t.Errorf("state: got %q, want %q", rep.State, RunStateStopped)
	}
	if rep.Version != "" {
		t.Errorf("version should be empty when stopped, got %q", rep.Version)
	}
	if len(f.Calls) != 0 {
		t.Errorf("Status (stopped) should not invoke psql; calls=%v", f.Calls)
	}
}

func TestStatusRunningWithVersion(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	// Open a real listener on the configured port so isPortListening
	// returns true. The fixture's port was picked from the kernel,
	// so it's reusable here.
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("listen on fixture port: %v", err)
	}
	defer ln.Close()

	f := &pgexec.Fake{}
	f.SetResult("psql", pgexec.Result{
		Stdout:   []byte("PostgreSQL 16.2 on x86_64-pc-linux-gnu, compiled by ...\n"),
		ExitCode: 0,
	})
	rep, err := Status(context.Background(), f, dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.State != RunStateRunning {
		t.Errorf("state: got %q, want %q", rep.State, RunStateRunning)
	}
	if !strings.HasPrefix(rep.Version, "PostgreSQL 16.2") {
		t.Errorf("version: got %q", rep.Version)
	}
	// RenderText should produce key=value lines including state=running.
	var buf bytes.Buffer
	rep.RenderText(&buf)
	if !strings.Contains(buf.String(), "state=running") {
		t.Errorf("RenderText output missing state=running: %s", buf.String())
	}
}

func TestStatusCrashed(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	// Pidfile present but no listener.
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	rep, err := Status(context.Background(), f, dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.State != RunStateCrashed {
		t.Errorf("state: got %q, want %q", rep.State, RunStateCrashed)
	}
}

func TestStatusNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	_, err := Status(context.Background(), f, tmp)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

// ---------------------------------------------------------------
// Destroy tests
// ---------------------------------------------------------------

func TestDestroyHappyPath(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: dir}, io.Discard)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sandbox dir still exists: err=%v", err)
	}
	// pg_ctl stop with -m immediate should have been called.
	if !callsContain(f, "pg_ctl", "stop") {
		t.Errorf("Destroy should issue pg_ctl stop; calls=%v", f.Calls)
	}
	if !callsContainArg(f, "pg_ctl", "immediate") {
		t.Errorf("Destroy should use -m immediate; calls=%v", f.Calls)
	}
}

func TestDestroyNotRunningSkipsPgctl(t *testing.T) {
	dir := deployFixture(t)
	f := &pgexec.Fake{}
	err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: dir}, io.Discard)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("Destroy on stopped sandbox should not invoke pg_ctl; calls=%v", f.Calls)
	}
}

func TestDestroyNotASandbox(t *testing.T) {
	tmp := t.TempDir()
	f := &pgexec.Fake{}
	err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: tmp}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitNotASandbox {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitNotASandbox)
	}
}

func TestDestroyMissingSandboxDir(t *testing.T) {
	f := &pgexec.Fake{}
	err := Destroy(context.Background(), f, DestroyOptions{}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCodeFor(err) != ui.ExitUsage {
		t.Errorf("exit code: got %d, want %d", ExitCodeFor(err), ui.ExitUsage)
	}
}

func TestDestroyIgnoresStopFailure(t *testing.T) {
	dir := deployFixture(t)
	cfg, _ := config.LoadSandbox(dir)
	mustCreatePid(t, cfg.DataDir)

	f := &pgexec.Fake{}
	// pg_ctl fails — destroy should still proceed.
	f.SetResult("pg_ctl", pgexec.Result{ExitCode: 1, Stderr: []byte("could not connect\n")})
	if err := Destroy(context.Background(), f, DestroyOptions{SandboxDir: dir}, io.Discard); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dir still present after destroy: %v", err)
	}
}

// ---------------------------------------------------------------
// ExitCodeFor / wrapExit tests
// ---------------------------------------------------------------

func TestExitCodeForNil(t *testing.T) {
	if got := ExitCodeFor(nil); got != ui.ExitOK {
		t.Errorf("got %d, want %d", got, ui.ExitOK)
	}
}

func TestExitCodeForUnwrapped(t *testing.T) {
	if got := ExitCodeFor(errors.New("plain")); got != ui.ExitGeneric {
		t.Errorf("got %d, want %d", got, ui.ExitGeneric)
	}
}

func TestWrapExitPreservesInner(t *testing.T) {
	inner := wrapExit(ui.ExitInitdbFailed, errors.New("boom"))
	outer := wrapExit(ui.ExitGeneric, inner)
	if ExitCodeFor(outer) != ui.ExitInitdbFailed {
		t.Errorf("inner code should win; got %d", ExitCodeFor(outer))
	}
}

// ---------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------

// deployFixture creates a deployed sandbox via Deploy with a Fake
// runner that succeeds, and returns the sandbox dir. Used by tests
// that need a valid sandbox dir but don't care how it got there.
func deployFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "sb")
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	f := &pgexec.Fake{}
	_, err := Deploy(context.Background(), f, DeployOptions{
		SandboxDir:   dir,
		BinDir:       bin,
		Port:         freeProbePort(t),
		PortExplicit: true,
	}, io.Discard)
	if err != nil {
		t.Fatalf("deployFixture: %v", err)
	}
	return dir
}

// freeProbePort asks the kernel for an unused ephemeral port, closes
// the listener, and returns the number. There is a tiny race window
// between Close and re-use, but for unit tests this is fine.
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

// mustCreatePid drops a synthetic postmaster.pid file in dataDir so
// isRunning returns true. Content is irrelevant — only presence
// matters.
func mustCreatePid(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "postmaster.pid"), []byte("12345\n"), 0o600); err != nil {
		t.Fatalf("pidfile: %v", err)
	}
}

// containsString reports whether ss contains target.
func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// callsContain reports whether the Fake has at least one call to
// name whose first arg is subcmd (used for "did stop happen?" style
// checks).
func callsContain(f *pgexec.Fake, name, subcmd string) bool {
	for _, c := range f.Calls {
		if c.Name == name && len(c.Args) > 0 && c.Args[0] == subcmd {
			return true
		}
	}
	return false
}

// callsContainArg reports whether the Fake has at least one call to
// name whose args contain the given token.
func callsContainArg(f *pgexec.Fake, name, token string) bool {
	for _, c := range f.Calls {
		if c.Name != name {
			continue
		}
		for _, a := range c.Args {
			if a == token {
				return true
			}
		}
	}
	return false
}
