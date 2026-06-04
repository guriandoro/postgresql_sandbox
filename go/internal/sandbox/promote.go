// Promote a physical standby to a standalone primary (SPEC §6.8).
//
// Design choices worth flagging:
//
//   - We refuse on a stopped instance with ExitNotAStandby rather
//     than ExitPgctlFailed. SPEC §6.8 frames "is a standby" as the
//     precondition; a stopped instance cannot be confirmed to be a
//     standby, so it fails the precondition. Returning the same
//     code for "wrong role" and "wrong state" keeps the user-facing
//     contract simple: ExitNotAStandby means "promote can't proceed
//     because of this sandbox's current state".
//
//   - pg_is_in_recovery() polling uses bounded retry rather than an
//     unbounded loop. ~10s with 500ms steps is generous for the
//     normal case (Postgres typically leaves recovery in well under
//     a second after `pg_ctl promote`) and short enough that a
//     misbehaving instance doesn't hang the CLI.
//
//   - On success we clear cfg.Physical entirely and flip Role to
//     RolePrimary. SPEC §6.8 step 4 describes appending a
//     promotedAt timestamp; SchemaVersion 1 doesn't carry one (the
//     field isn't declared in schema.go), so we rely on
//     LastModifiedAt as the audit trail. Adding promotedAt would
//     require a SchemaVersion bump, which is out of scope here.

package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
)

// PromoteOptions captures the (small) input set for Promote.
type PromoteOptions struct {
	// SandboxDir is the standby being promoted.
	SandboxDir string
}

// promoteWaitTimeout is the upper bound on how long we wait for
// `pg_is_in_recovery()` to flip to false after `pg_ctl promote`. A
// real instance returns true within a few hundred milliseconds; the
// 10s bound is generous safety margin.
const promoteWaitTimeout = 10 * time.Second

// promotePollInterval is the gap between successive recovery-state
// probes during the post-promote wait.
const promotePollInterval = 500 * time.Millisecond

// Promote runs `pg_ctl promote -D <dataDir>` against a standby,
// then waits for the instance to leave recovery, then updates the
// sandbox config to record the new role. Returns nil on a complete
// promotion.
//
// Documented failure modes:
//
//   - ExitNotASandbox: the target is not a sandbox dir.
//   - ExitNotAStandby: the sandbox is a non-standby role, or is
//     stopped (we can't promote what isn't running).
//   - ExitPromoteFailed: pg_ctl promote returned non-zero, or the
//     post-promote recovery-state wait timed out.
func Promote(ctx context.Context, runner pgexec.Runner, opts PromoteOptions, stderrW io.Writer) error {
	cfg, err := loadSandboxOrFail(opts.SandboxDir)
	if err != nil {
		return err
	}
	if cfg.Role != config.RoleStandby {
		return wrapExit(ExitNotAStandby,
			fmt.Errorf("sandbox %q has role %q, not standby",
				cfg.Name, cfg.Role))
	}
	if !isRunning(cfg) {
		return wrapExit(ExitNotAStandby,
			fmt.Errorf("sandbox %q is not running; promote requires a live standby", cfg.Name))
	}

	res := runner.Run(ctx, "pg_ctl",
		"promote",
		"-D", cfg.DataDir,
		"-w",
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "pg_ctl promote", res.Stderr)
		return wrapExit(ExitPromoteFailed,
			fmt.Errorf("pg_ctl promote exit=%d: %w", res.ExitCode, res.Err))
	}

	// Wait until pg_is_in_recovery() returns 'f'. We use a fresh
	// context for the wait so the caller's context cancellation
	// short-circuits the loop, but a slow PG doesn't burn through the
	// full caller-deadline either.
	if err := waitOutOfRecovery(ctx, runner, cfg, stderrW); err != nil {
		return err
	}

	// Update on-disk config. We Validate first so a malformed
	// resulting struct (shouldn't happen — we only flip two fields)
	// errors cleanly rather than corrupting the file.
	cfg.Role = config.RolePrimary
	cfg.Physical = nil
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("sandbox.Promote: validate: %w", err)
	}
	if err := config.SaveSandbox(opts.SandboxDir, cfg); err != nil {
		return fmt.Errorf("sandbox.Promote: save config: %w", err)
	}
	fmt.Fprintf(stderrW, "level=INFO msg=\"promoted standby to primary\" name=%q\n", cfg.Name)
	return nil
}

// waitOutOfRecovery polls SELECT pg_is_in_recovery() at
// promotePollInterval until it returns 'f' or promoteWaitTimeout
// elapses. Returns ExitPromoteFailed on timeout or repeated psql
// failure.
func waitOutOfRecovery(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, stderrW io.Writer) error {
	deadline := time.Now().Add(promoteWaitTimeout)
	for {
		res := runner.Run(ctx, "psql",
			"-X", "-A", "-t",
			"-h", cfg.Host,
			"-p", fmt.Sprintf("%d", cfg.Port),
			"-U", cfg.Superuser,
			"-d", cfg.DefaultDatabase,
			"-c", "SELECT pg_is_in_recovery();",
		)
		if res.Err == nil && res.ExitCode == 0 {
			out := strings.TrimSpace(string(res.Stdout))
			// psql -A -t returns "f" or "t" for a boolean.
			if out == "f" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			emitStderr(stderrW, "psql pg_is_in_recovery", res.Stderr)
			return wrapExit(ExitPromoteFailed,
				fmt.Errorf("timed out waiting for sandbox %q to leave recovery after %s",
					cfg.Name, promoteWaitTimeout))
		}
		select {
		case <-ctx.Done():
			return wrapExit(ExitPromoteFailed,
				fmt.Errorf("promote wait cancelled: %w", ctx.Err()))
		case <-time.After(promotePollInterval):
		}
	}
}
