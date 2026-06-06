// Logical-subscriber deploy path (SPEC §6.1, the --subscribe-to
// branch).
//
// This is a thin combinator: it runs the standalone deploy first
// (fresh initdb + start), then calls Subscribe against the resolved
// publisher. The split keeps responsibilities clean — the standalone
// path doesn't grow logical-replication awareness, and Subscribe
// stays the single source of truth for "create a subscription on
// THIS sandbox attached to THAT one".
//
// Failure modes:
//
//   - The standalone deploy can fail with its usual codes
//     (ExitInitdbFailed, ExitPgctlFailed, ExitPortInUse,
//     ExitSandboxExists). Those propagate unchanged.
//
//   - The subscribe step can fail with ExitSourceUnreachable (the
//     publisher isn't running), ExitSchemaCopyFailed (--copy-schema
//     pg_dump|psql failed), or ExitSubscriptionFailed (CREATE
//     SUBSCRIPTION returned non-zero). On any of these we LEAVE the
//     freshly-deployed sandbox in place — the user can inspect it
//     and re-run `subscribe` manually. This mirrors the physical
//     standby path, which doesn't undeploy on basebackup failure.

package sandbox

import (
	"context"
	"fmt"
	"io"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// deploySubscriber implements SPEC §6.1's logical-subscriber code
// path. Called from Deploy when DeployOptions.SubscribeTo is non-empty.
func deploySubscriber(ctx context.Context, runner pgexec.Runner, opts DeployOptions, stderrW io.Writer) (*DeployResult, error) {
	if opts.PubName == "" {
		return nil, wrapExit(ExitUsage,
			fmt.Errorf("sandbox.Deploy: --pub-name is required when --subscribe-to is set"))
	}

	// Step 1: standalone deploy. We pass through opts unchanged
	// because all fields the standalone path cares about (port,
	// host, dbname, ...) are independently set by the user and
	// belong to the new subscriber sandbox, not the publisher.
	res, err := deployStandalone(ctx, runner, opts, stderrW)
	if err != nil {
		return nil, err
	}

	// Step 2: subscribe. We build SubscribeOptions from the same
	// DeployOptions and call the shared Subscribe entry point — that
	// way the on-disk Logical block + config validation lives in
	// exactly one place.
	subOpts := SubscribeOptions{
		SandboxDir:   opts.SandboxDir,
		PublisherRef: opts.SubscribeTo,
		PubName:      opts.PubName,
		SubName:      opts.SubName,
		Dbname:       opts.Dbname,
		CopySchema:   opts.CopySchema,
		NoCopyData:   opts.NoCopyData,
	}
	if err := Subscribe(ctx, runner, subOpts, stderrW); err != nil {
		// SPEC §6.1 step 7 doesn't say to undeploy on subscribe
		// failure; the brief explicitly leaves the sandbox in place
		// (matches the physical standby path). The user sees the
		// error and can re-run `subscribe` after fixing the
		// underlying cause (e.g., starting the publisher).
		return nil, err
	}

	// Subscribe wrote a new config on disk (Role=subscriber +
	// Logical block populated). Reload so DeployResult reflects what
	// callers see if they immediately re-read the on-disk file.
	reloaded, err := config.LoadSandbox(opts.SandboxDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox.deploySubscriber: reload after subscribe: %w", err)
	}
	res.Sandbox = reloaded

	fmt.Fprintf(stderrW, "level=INFO msg=%q name=%q publisher=%q pub=%q\n",
		"deployed subscriber", res.Sandbox.Name, opts.SubscribeTo, opts.PubName)
	return res, nil
}
