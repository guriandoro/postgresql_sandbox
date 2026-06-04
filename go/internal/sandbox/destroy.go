// Destroy: stop the sandbox's postgres and remove the sandbox dir.
// SPEC §6.3.
//
// This slice covers the standalone-sandbox path only. The
// best-effort slot/subscription cleanup at upstream sources (SPEC
// §6.3 step 3) is replication territory and lands with the
// replication slices.
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

	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
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
// moment regardless).
func Destroy(ctx context.Context, runner pgexec.Runner, opts DestroyOptions, stderrW io.Writer) error {
	if opts.SandboxDir == "" {
		return wrapExit(ExitUsage, errors.New("sandbox.Destroy: SandboxDir is required"))
	}
	cfg, err := loadSandboxOrFail(opts.SandboxDir)
	if err != nil {
		return err
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
