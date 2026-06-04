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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/portalloc"
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

	// SPEC §6.2: "already running" is a no-op success. We check the
	// pidfile (cheap, no fork). A pidfile-but-port-dead state is
	// suspect and reported by Status; for Start we treat any pidfile
	// presence as "already up" and let pg_ctl's own idempotency
	// handle the corner case if a user retries.
	if isRunning(cfg) {
		fmt.Fprintf(stderrW, "level=INFO msg=\"already running\" name=%q port=%d\n", cfg.Name, cfg.Port)
		return nil
	}

	res := runner.Run(ctx, "pg_ctl",
		"start",
		"-D", cfg.DataDir,
		"-l", cfg.LogFile,
		// We DO NOT re-supply -o "-h ... -p ..." on start (only on
		// the initial deploy). After deploy, postgres has the right
		// command-line baked into its first start and reloads
		// postgresql.conf on subsequent ones. (pg_ctl actually
		// re-uses the original command line via postmaster.opts.)
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
// exists. We deliberately do NOT also probe the port here — a
// pidfile-present-but-port-dead state is an "unhealthy/crashed"
// condition that Status surfaces separately; Start/Stop just want
// "should I bother shelling out to pg_ctl".
func isRunning(cfg *config.Sandbox) bool {
	if cfg == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(cfg.DataDir, "postmaster.pid"))
	return err == nil
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
