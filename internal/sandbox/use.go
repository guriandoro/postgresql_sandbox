// `use` — exec psql against a sandbox. SPEC §6.5.
//
// The work this file owns is purely argv/env construction:
//
//   - Load the sandbox config (refuse non-sandbox dirs per SPEC §4.2).
//   - Build the psql argv: -h <host> -p <port> -U <superuser>
//     -d <defaultDatabase> followed by whatever the user passed
//     after `--` on the command line, forwarded verbatim.
//   - Build the PG* env vars (PGHOST/PGPORT/PGUSER/PGDATABASE) so
//     that any sub-process psql spawns (e.g. \! shell scripts that
//     call psql again) inherits the same connection target.
//
// The actual syscall.Exec lives in the CLI layer (cmd/pg_sandbox)
// because Runner.Exec replaces the current process and never
// returns on success — keeping the exec call right next to the
// caller's main() makes that control-flow truth obvious. This
// package returns a *UseInvocation describing WHAT to exec; the
// caller wires up runner.Env and calls runner.Exec.
//
// This split also makes the argv/env construction unit-testable
// without an actual fork/exec. Tests assert on the returned
// UseInvocation struct directly — no fake-Runner gymnastics.
//
// We deliberately do NOT propagate PGPASSWORD here. The sandbox
// uses trust auth (SPEC §11 q2 resolution), so passwords are
// never needed for local 127.0.0.1 connections. If a user has
// PGPASSWORD in their environment for some other reason, the OS
// environment is inherited normally by syscall.Exec (the runner
// builds the child env as os.Environ() + runner.Env), so that
// case still works without any explicit handling here.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// UseBinary is the binary name PrepareUse targets. Exposed as a
// constant so the CLI layer and tests reference the same value;
// hard-coded "psql" string literals would drift.
const UseBinary = "psql"

// UseInvocation is the argv + env description PrepareUse returns.
// The caller is expected to attach Env to a *pgexec.Exec and then
// invoke runner.Exec(Binary, Args...).
type UseInvocation struct {
	// Binary is the name of the binary to exec (always "psql" for
	// `use`). Stored as a field rather than a constant so the same
	// struct shape can be reused if a future variant of `use`
	// targets a different psql-compatible tool.
	Binary string

	// Args is the argv after the binary name. The first four
	// elements are always -h/-p/-U/-d carrying the sandbox's
	// resolved connection target; anything after is the user-
	// supplied forwarded args (everything after `--` on the CLI).
	Args []string

	// Env is the slice of KEY=VALUE strings to set on the child
	// process via *pgexec.Exec.Env. We always set PGHOST, PGPORT,
	// PGUSER, PGDATABASE — even though -h/-p/-U/-d are also on
	// argv — so that any psql sub-process (e.g. spawned from a
	// \! shell command) inherits the same connection target
	// without re-specifying flags.
	Env []string
}

// PrepareUse loads the sandbox at dir, validates it, and returns
// the UseInvocation that the CLI layer will hand to runner.Exec.
//
// extraArgs is the argv tail forwarded verbatim to psql (typically
// everything the user passed after `--` on the command line). It
// MAY be nil or empty — the bare `use -s X` case just gives the
// user an interactive psql session.
//
// On a non-sandbox dir → wrapExit(ExitNotASandbox).
//
// ctx is accepted for symmetry with the other Prepare/operation
// functions in this package and to future-proof the signature; the
// current implementation does not block on anything that takes a
// context, but a future addition (e.g. resolving cluster metadata
// to honor --replica-of routing) would.
func PrepareUse(ctx context.Context, dir string, extraArgs []string) (*UseInvocation, error) {
	if dir == "" {
		return nil, wrapExit(ExitUsage, errors.New("sandbox.PrepareUse: dir is required"))
	}
	_ = ctx // accepted for future-proofing; see doc comment.

	cfg, err := loadSandboxOrFail(dir)
	if err != nil {
		return nil, err
	}

	// Argv: -h, -p, -U, -d first (so they appear in the argv if
	// the user wants to grep `ps` output) then the user's tail.
	// extraArgs is appended raw — psql's own flag parser handles
	// later -d / -U etc. overriding our earlier ones (libpq /
	// psql last-flag-wins semantics), which is the behavior users
	// expect when they pass an explicit override.
	args := make([]string, 0, 8+len(extraArgs))
	args = append(args,
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
	)
	args = append(args, extraArgs...)

	return &UseInvocation{
		Binary: UseBinary,
		Args:   args,
		Env:    pgConnEnv(cfg.Host, cfg.Port, cfg.Superuser, cfg.DefaultDatabase),
	}, nil
}

// pgConnEnv builds the KEY=VALUE slice used for PGHOST/PGPORT/
// PGUSER/PGDATABASE. Factored out so `run` (SPEC §6.6) shares the
// same env shape — both commands must agree on the env contract
// they expose to child processes.
func pgConnEnv(host string, port int, user, dbname string) []string {
	return []string{
		"PGHOST=" + host,
		"PGPORT=" + strconv.Itoa(port),
		"PGUSER=" + user,
		"PGDATABASE=" + dbname,
	}
}

// LocateUseBinary resolves the psql binary via the runner's
// Locate. Exposed as a separate function so the CLI layer can
// short-circuit with a clear "psql not found" error BEFORE
// calling runner.Exec — syscall.Exec failing inside the OS would
// produce a much more cryptic message.
func LocateUseBinary(runner pgexec.Runner) (string, error) {
	p, err := runner.Locate(UseBinary)
	if err != nil {
		return "", fmt.Errorf("sandbox.PrepareUse: locate psql: %w", err)
	}
	return p, nil
}
