// Exit codes for pg_sandbox.
//
// These constants are the single source of truth for the exit code
// surface documented in SPEC.md §8 and go/docs/exit-codes.md. The
// linker can't enforce that those three places stay in sync, so when
// you change a code here you MUST also update both docs. There's a
// table-driven test (TestExitCodesMatchSpec in exit_test.go) that
// will fail if the numeric values here drift from what the spec
// table claims, which gives a CI-time tripwire.

package ui

// ExitCode is a typed exit status. It exists as a named type rather
// than a raw int so callers passing one to os.Exit() can't
// accidentally pass an unrelated int that happens to be in range.
type ExitCode int

// Int returns the underlying int suitable for os.Exit. Defined as a
// method (rather than letting callers cast) so the call site reads
// `os.Exit(int(code))` only at the program boundary.
func (c ExitCode) Int() int { return int(c) }

const (
	// ExitOK is the success code. Many commands return ExitOK for
	// documented no-ops (e.g., `start` on an already-running
	// sandbox); not-running is a state, not a failure.
	ExitOK ExitCode = 0

	// ExitGeneric is the unclassified fallback. We try to avoid
	// returning it — every real failure should map to a specific
	// code below. If you find yourself returning ExitGeneric,
	// consider adding a new constant.
	ExitGeneric ExitCode = 1

	// ExitUsage covers bad CLI usage: unknown flag, unknown
	// command, missing required argument. Mirrors getopt
	// convention so shell scripts that expect 2 for "you held it
	// wrong" keep working.
	ExitUsage ExitCode = 2

	// ExitNotASandbox: the --sandbox-dir target doesn't contain
	// the canonical config file. The tool refuses to treat any
	// other directory as a sandbox (SPEC §4.2).
	ExitNotASandbox ExitCode = 3

	// ExitNotACluster: the --sandbox-dir target doesn't contain
	// the cluster manifest file.
	ExitNotACluster ExitCode = 4

	// ExitSandboxExists: deploy target already populated. We never
	// silently overwrite a sandbox dir.
	ExitSandboxExists ExitCode = 5

	// ExitClusterExists: cluster deploy target already populated.
	ExitClusterExists ExitCode = 6

	// ExitBadConfig: the on-disk config file is malformed,
	// unknown-key, or has a schemaVersion the binary doesn't
	// understand.
	ExitBadConfig ExitCode = 7

	// ExitConfigKeyUnknown: `config set` or `config get` named a
	// key that isn't declared in the schema.
	ExitConfigKeyUnknown ExitCode = 8

	// ExitPortInUse: --port was supplied explicitly and the port
	// is busy. Without explicit --port, the tool auto-allocates;
	// see ExitNoFreePort for the exhaustion case.
	ExitPortInUse ExitCode = 9

	// ExitNoFreePort: auto-allocation walked the entire configured
	// port range without finding a free one.
	ExitNoFreePort ExitCode = 10

	// ExitInitdbFailed: `initdb` returned non-zero. The error
	// message includes the server log path so users can read what
	// went wrong.
	ExitInitdbFailed ExitCode = 11

	// ExitPgctlFailed: `pg_ctl` (start/stop/restart/promote)
	// returned non-zero.
	ExitPgctlFailed ExitCode = 12

	// ExitBasebackupFailed: `pg_basebackup` failed during a
	// physical standby deploy.
	ExitBasebackupFailed ExitCode = 13

	// ExitSourceUnreachable: the replication/subscription source
	// sandbox isn't reachable (down, wrong port, network).
	ExitSourceUnreachable ExitCode = 14

	// ExitPublicationFailed: `CREATE PUBLICATION` errored on the
	// server.
	ExitPublicationFailed ExitCode = 15

	// ExitSubscriptionFailed: `CREATE SUBSCRIPTION` errored on the
	// server.
	ExitSubscriptionFailed ExitCode = 16

	// ExitSchemaCopyFailed: `pg_dump --schema-only` failed during
	// `subscribe --copy-schema`.
	ExitSchemaCopyFailed ExitCode = 17

	// ExitNotAStandby: `promote` called on something that isn't a
	// physical standby.
	ExitNotAStandby ExitCode = 18

	// ExitPromoteFailed: `pg_ctl promote` was issued but the
	// instance didn't leave recovery within the timeout.
	ExitPromoteFailed ExitCode = 19

	// ExitDestroyFailed: rm of the sandbox dir failed (permission
	// denied, busy mountpoint).
	ExitDestroyFailed ExitCode = 20

	// ExitClusterDeployFailed: one or more cluster members failed
	// to deploy. Partially deployed members are left in place for
	// inspection.
	ExitClusterDeployFailed ExitCode = 21

	// ExitClusterDestroyPartial: one or more cluster members
	// survived destroy. Cluster dir is preserved with the
	// manifest so the user can finish manually.
	ExitClusterDestroyPartial ExitCode = 22

	// ExitPgGatherDirMissing: `report` needs the pg_gather scripts
	// directory and didn't find it.
	ExitPgGatherDirMissing ExitCode = 23

	// ExitReportFailed: `report` pipeline failed somewhere after
	// the throwaway sandbox was created.
	ExitReportFailed ExitCode = 24

	// ExitPsqlFailed: a `psql` invocation failed unexpectedly
	// (server crash, connection drop). General-purpose for
	// in-band SQL failures.
	ExitPsqlFailed ExitCode = 25

	// ExitInterrupted: the tool caught SIGINT or SIGTERM and is
	// exiting mid-operation. Distinct code so scripts can tell
	// "you Ctrl-C'd me" from a real failure.
	ExitInterrupted ExitCode = 26

	// ExitNotATTY: a confirmation prompt was needed, --force
	// wasn't set, and stdin isn't a TTY. We refuse rather than
	// silently aborting or silently proceeding.
	ExitNotATTY ExitCode = 27

	// ExitRestartRequiredRefused is reserved for a future
	// --no-restart flag on `publish`/`subscribe`. Not currently
	// returned by any code path; reserved so the number is stable.
	ExitRestartRequiredRefused ExitCode = 28

	// ExitBuildFailed (Phase 2): source build failed.
	ExitBuildFailed ExitCode = 30
)
