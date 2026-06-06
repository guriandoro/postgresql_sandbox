// Destroy: stop the sandbox's postgres and remove the sandbox dir.
// SPEC §6.3.
//
// Standalone destroy is just stop + rm. The replication slice adds
// a best-effort step BEFORE rm: if the sandbox is a physical
// standby, drop its slot on the source. Failure here is a warning,
// not an error — SPEC §6.3 step 3 explicitly says cleanup at the
// source doesn't block destroy. The data dir is going away regardless,
// so a stale slot is mildly annoying but not catastrophic.
//
// Confirmation prompts (SPEC §4.7) live in the CLI layer, not here:
// keeping prompts out of internal/sandbox lets unit tests run
// without TTY hooks and lets the CLI choose how to render the
// prompt. Destroy itself takes a `confirmed bool` and trusts the
// caller — its only contract is "if confirmed, actually destroy".

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// DestroyOptions captures the (small) input set for Destroy.
type DestroyOptions struct {
	// SandboxDir is the directory to remove.
	SandboxDir string
}

// Destroy stops the postgres instance (immediate-mode, no graceful
// flush — the caller asked to destroy) and removes the sandbox
// directory and everything under it.
//
// A non-sandbox dir returns wrapExit(ExitNotASandbox). A rm failure
// returns wrapExit(ExitDestroyFailed). Failure to stop is logged but
// does NOT block destroy (the data dir is going to disappear in a
// moment regardless). Failure to drop the upstream slot for a
// standby is also logged-and-continue.
func Destroy(ctx context.Context, runner pgexec.Runner, opts DestroyOptions, stderrW io.Writer) error {
	if opts.SandboxDir == "" {
		return wrapExit(ExitUsage, errors.New("sandbox.Destroy: SandboxDir is required"))
	}
	cfg, err := loadSandboxOrFail(opts.SandboxDir)
	if err != nil {
		return err
	}

	// SPEC §6.3 step 3 (logical half): best-effort DROP SUBSCRIPTION
	// while the instance is still up. Doing it BEFORE the stop lets
	// us use the local socket; doing it after pg_ctl stop would
	// require restarting just to drop, which the user didn't ask
	// for. All errors here are warnings — the data dir is going to
	// disappear regardless.
	if cfg.Logical != nil && cfg.Logical.SubscriptionName != "" && isRunning(cfg) {
		bestEffortDropSubscription(ctx, runner, cfg, stderrW)
	}

	if isRunning(cfg) {
		// SPEC §6.3 step 2: immediate-mode stop. We ignore the exit
		// code/err — we are about to rm -rf the data dir, and any
		// stop failure is moot. Emit the stderr so the user can see
		// what happened if they're debugging.
		res := runner.Run(ctx, "pg_ctl",
			"stop",
			"-D", cfg.DataDir,
			"-m", "immediate",
			"-w",
		)
		if res.Err != nil || res.ExitCode != 0 {
			emitStderr(stderrW, "pg_ctl stop (during destroy)", res.Stderr)
			fmt.Fprintf(stderrW, "level=WARN msg=%q name=%q\n",
				"stop failed during destroy; proceeding with rm anyway", cfg.Name)
		}
	}

	// SPEC §6.3 step 3: best-effort cleanup at the upstream. Only
	// relevant for physical standbys (logical subscribers land in a
	// later slice). All errors here are warnings, never fatal.
	if cfg.Physical != nil && cfg.Physical.SlotName != "" {
		bestEffortDropSlot(ctx, runner, opts.SandboxDir, cfg.Physical, stderrW)
	}

	// SPEC §6.3 step 4: rm -rf. os.RemoveAll handles non-existent
	// targets silently (nil error) which is what we want — the prior
	// loadSandboxOrFail already established the dir exists, so any
	// RemoveAll error here is a real permissions/mount issue.
	if err := os.RemoveAll(opts.SandboxDir); err != nil {
		return wrapExit(ExitDestroyFailed, fmt.Errorf("rm %s: %w", opts.SandboxDir, err))
	}
	fmt.Fprintf(stderrW, "level=INFO msg=\"destroyed\" name=%q dir=%q\n",
		cfg.Name, opts.SandboxDir)
	return nil
}

// bestEffortDropSlot attempts to drop the replication slot
// physical.SlotName on the source sandbox referenced by
// physical.SourceSandbox. The slot is dropped via
// pg_drop_replication_slot, guarded by an EXISTS clause so a missing
// slot is a no-op rather than an error.
//
// Every failure mode is a warning. The caller is mid-destroy: the
// sandbox dir is about to disappear; the most we can do is tell the
// user "you may have a stale slot at <source>" and proceed.
//
// We reuse the caller's runner rather than constructing a new
// pgexec.Exec pointed at srcCfg.BinDir for the same reasons
// deploy_standby.go reuses its runner: in practice the installs
// match, and tests need a SINGLE Fake intercepting every call.
func bestEffortDropSlot(ctx context.Context, runner pgexec.Runner, sandboxDir string, phys *config.Physical, stderrW io.Writer) {
	srcDir, err := resolveSourceSandbox(sandboxDir, phys.SourceSandbox)
	if err != nil {
		fmt.Fprintf(stderrW,
			"level=WARN msg=%q slot=%q source=%q err=%q\n",
			"slot cleanup skipped: source not resolvable",
			phys.SlotName, phys.SourceSandbox, err.Error())
		return
	}
	srcCfg, err := config.LoadSandbox(srcDir)
	if err != nil {
		fmt.Fprintf(stderrW,
			"level=WARN msg=%q slot=%q source=%q err=%q\n",
			"slot cleanup skipped: cannot load source config",
			phys.SlotName, phys.SourceSandbox, err.Error())
		return
	}
	if !isRunning(srcCfg) || !isPortListening(srcCfg) {
		fmt.Fprintf(stderrW,
			"level=WARN msg=%q slot=%q source=%q\n",
			"slot cleanup skipped: source not running",
			phys.SlotName, phys.SourceSandbox)
		return
	}

	// The query is guarded by EXISTS so dropping a missing slot is a
	// no-op rather than a SQL error. We reuse the caller's runner
	// (see function-level doc comment for why).
	srcRunner := runner
	query := fmt.Sprintf(
		"SELECT pg_drop_replication_slot('%s') WHERE EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name='%s');",
		phys.SlotName, phys.SlotName)
	res := srcRunner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", srcCfg.Host,
		"-p", fmt.Sprintf("%d", srcCfg.Port),
		"-U", srcCfg.Superuser,
		"-d", "postgres",
		"-c", query,
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "psql drop slot", res.Stderr)
		fmt.Fprintf(stderrW,
			"level=WARN msg=%q slot=%q source=%q\n",
			"slot cleanup failed; you may have a stale slot at source",
			phys.SlotName, phys.SourceSandbox)
		return
	}
	fmt.Fprintf(stderrW,
		"level=INFO msg=%q slot=%q source=%q\n",
		"dropped replication slot at source",
		phys.SlotName, phys.SourceSandbox)
}

// bestEffortDropSubscription disables and drops the local
// subscription before the sandbox dir is removed. Three statements
// are issued as one psql -c so we make a single round-trip:
//
//  1. ALTER SUBSCRIPTION ... DISABLE — stop the worker; without
//     this, DROP can race with the apply worker.
//  2. ALTER SUBSCRIPTION ... SET (slot_name = NONE) — detach the
//     subscription from its remote slot so DROP doesn't try to
//     contact the publisher to drop the remote slot. By destroy
//     time the publisher might be unreachable, deleted, or simply
//     not the destination's concern; NONE makes DROP local-only and
//     leaves any orphan slot on the publisher for the user to clean
//     up via `pg_drop_replication_slot` if they care.
//  3. DROP SUBSCRIPTION — the local catalog row goes away.
//
// All errors are warn-level. The data dir is about to be rm'd, so a
// stranded local subscription row is meaningless; the only
// observable consequence of a failure here is a possible orphan slot
// on the publisher.
func bestEffortDropSubscription(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, stderrW io.Writer) {
	sub := cfg.Logical.SubscriptionName
	// SET (slot_name = NONE) THEN DROP — see function-level comment
	// for why. We chain via semicolons in a single psql -c so the
	// three statements share a transaction-scoped session and we
	// pay one auth round-trip.
	stmt := fmt.Sprintf(
		"ALTER SUBSCRIPTION %s DISABLE; ALTER SUBSCRIPTION %s SET (slot_name = NONE); DROP SUBSCRIPTION %s;",
		sub, sub, sub)
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.Logical.TargetDatabase,
		"-c", stmt,
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "psql DROP SUBSCRIPTION", res.Stderr)
		fmt.Fprintf(stderrW,
			"level=WARN msg=%q subscription=%q name=%q\n",
			"subscription cleanup failed; the local row goes away with the data dir but the publisher slot may remain",
			sub, cfg.Name)
		return
	}
	fmt.Fprintf(stderrW,
		"level=INFO msg=%q subscription=%q name=%q\n",
		"dropped local subscription before destroy",
		sub, cfg.Name)
}
