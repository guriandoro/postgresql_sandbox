// Lifecycle operations (start / stop / restart) for an
// already-deployed sandbox. SPEC §6.2.
//
// These functions share enough plumbing (load config, refuse
// non-sandbox dirs, locate pg_ctl) that splitting them into separate
// files would create more boilerplate than it removed. The
// individual flows are tiny — each one delegates the heavy lifting
// to pg_ctl and reports the outcome.
//
// The parent-scan mode for `stop` (SPEC §6.2.2) is deliberately not
// implemented in this slice; the CLI layer just calls Stop on a
// single sandbox dir. When parent-scan lands, it will walk children
// in the CLI layer and call Stop on each.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/portalloc"
)

// Start runs `pg_ctl start` against the sandbox at dir. If the
// instance is already running, Start logs an info-level no-op and
// returns nil (SPEC §6.2: "already running" is exit 0, not an
// error).
func Start(ctx context.Context, runner pgexec.Runner, dir string, stderrW io.Writer) error {
	cfg, err := loadSandboxOrFail(dir)
	if err != nil {
		return err
	}

	// SPEC §6.2: "already running" is a no-op success. isRunning
	// checks the pidfile AND that the recorded PID is alive, so a
	// stale pidfile left by a reboot or kill -9 does not
	// short-circuit the start — pg_ctl runs and applies its own
	// stale-pid handling. A pid-alive-but-port-dead state is suspect
	// and reported by Status; for Start we treat a live PID as
	// "already up".
	if isRunning(cfg) {
		fmt.Fprintf(stderrW, "level=INFO msg=\"already running\" name=%q port=%d\n", cfg.Name, cfg.Port)
		return nil
	}

	// We re-supply `-o "-h <host> -p <port>"` on EVERY start, not
	// only on the initial deploy. Verified empirically: pg_ctl
	// rewrites postmaster.opts on each `pg_ctl start` from whatever
	// args YOU pass; if you pass no `-o`, the rewritten file lacks
	// `-h`/`-p` and the next postgres falls back to its compiled-in
	// defaults (port 5432, IPv6+IPv4 wildcard). Without this line,
	// `restart` silently moves the sandbox onto the wrong port.
	pgctlOpts := fmt.Sprintf("-h %s -p %d", cfg.Host, cfg.Port)
	res := runner.Run(ctx, "pg_ctl",
		"start",
		"-D", cfg.DataDir,
		"-l", cfg.LogFile,
		"-o", pgctlOpts,
		"-w",
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "pg_ctl start", res.Stderr)
		return wrapExit(ExitPgctlFailed, fmt.Errorf("pg_ctl start exit=%d: %w", res.ExitCode, res.Err))
	}
	fmt.Fprintf(stderrW, "level=INFO msg=\"started\" name=%q host=%q port=%d\n",
		cfg.Name, cfg.Host, cfg.Port)
	return nil
}

// Stop runs `pg_ctl stop -m fast`. Not-running is a no-op success.
func Stop(ctx context.Context, runner pgexec.Runner, dir string, stderrW io.Writer) error {
	cfg, err := loadSandboxOrFail(dir)
	if err != nil {
		return err
	}
	if !isRunning(cfg) {
		// A pidfile whose PID is dead (host reboot, kill -9) would
		// make `pg_ctl stop` fail and wedge Restart before it ever
		// reaches Start. Remove it and treat Stop as a no-op success.
		pidPath := pidfilePath(cfg)
		if _, statErr := os.Stat(pidPath); statErr == nil {
			fmt.Fprintf(stderrW, "level=WARN msg=\"stale postmaster.pid, removing\" name=%q path=%q\n", cfg.Name, pidPath)
			if rmErr := os.Remove(pidPath); rmErr != nil {
				fmt.Fprintf(stderrW, "level=WARN msg=\"stale pidfile removal failed\" name=%q error=%q\n", cfg.Name, rmErr)
			}
		}
		fmt.Fprintf(stderrW, "level=INFO msg=\"not running\" name=%q\n", cfg.Name)
		return nil
	}
	res := runner.Run(ctx, "pg_ctl",
		"stop",
		"-D", cfg.DataDir,
		"-m", "fast",
		"-w",
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "pg_ctl stop", res.Stderr)
		return wrapExit(ExitPgctlFailed, fmt.Errorf("pg_ctl stop exit=%d: %w", res.ExitCode, res.Err))
	}
	fmt.Fprintf(stderrW, "level=INFO msg=\"stopped\" name=%q\n", cfg.Name)
	return nil
}

// Restart stops then starts. We do not use `pg_ctl restart` because
// SPEC §6.2 frames restart as "stop then start" — keeping the two
// halves explicit means a Stop failure is reported with the right
// exit code, and a no-op Stop (already-stopped) still triggers a
// Start.
func Restart(ctx context.Context, runner pgexec.Runner, dir string, stderrW io.Writer) error {
	if err := Stop(ctx, runner, dir, stderrW); err != nil {
		return err
	}
	return Start(ctx, runner, dir, stderrW)
}

// loadSandboxOrFail refuses a non-sandbox dir per SPEC §4.2 and
// returns the parsed config on success.
func loadSandboxOrFail(dir string) (*config.Sandbox, error) {
	if !config.IsSandboxDir(dir) {
		return nil, wrapExit(ExitNotASandbox, fmt.Errorf("not a sandbox: %s", dir))
	}
	cfg, err := config.LoadSandbox(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: load config: %w", err)
	}
	return cfg, nil
}

// isRunning is a cheap "is postgres up" check used by Start, Stop,
// and Status. It returns true iff the data dir's postmaster.pid
// exists AND the PID on its first line is alive (signal-0 probe;
// EPERM counts as alive). A missing, unparsable, or dead-PID pidfile
// all mean "not running" — a stale pidfile left by a reboot or
// kill -9 must not wedge Start/Restart. We deliberately do NOT also
// probe the port here — a pid-alive-but-port-dead state is an
// "unhealthy/crashed" condition that Status surfaces separately;
// Start/Stop just want "should I bother shelling out to pg_ctl".
func isRunning(cfg *config.Sandbox) bool {
	if cfg == nil {
		return false
	}
	pid, err := readPidfile(pidfilePath(cfg))
	if err != nil {
		return false
	}
	return pidAlive(pid)
}

// pidfilePath returns the sandbox's postmaster.pid path.
func pidfilePath(cfg *config.Sandbox) string {
	return filepath.Join(cfg.DataDir, "postmaster.pid")
}

// readPidfile reads the first line of a postmaster.pid file and
// parses it as a PID. A missing file or garbage content comes back
// as an error — callers treat both as "not running".
func readPidfile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	first, _, _ := strings.Cut(string(b), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("unparsable pidfile %s: first line %q", path, first)
	}
	return pid, nil
}

// pidAlive probes pid with signal 0. nil and EPERM both mean a
// process exists (EPERM = alive but owned by someone else); ESRCH —
// or any other failure — means it does not.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// isPortListening returns true if something is listening on the
// sandbox's host:port. Used by Status to distinguish "running" from
// "pidfile present but crashed".
func isPortListening(cfg *config.Sandbox) bool {
	if cfg == nil {
		return false
	}
	busy, _ := portalloc.IsBusy(cfg.Host, cfg.Port)
	return busy
}

// emitStderr writes a single structured line summarising the stderr
// captured from a failed child process. We trim trailing newlines so
// the key=value line stays on a single physical line.
func emitStderr(w io.Writer, what string, b []byte) {
	if len(b) == 0 {
		return
	}
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return
	}
	fmt.Fprintf(w, "level=ERROR msg=%q output=%q\n", what+" stderr", trimmed)
}
