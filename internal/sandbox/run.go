// `run` — exec an arbitrary PG utility against a sandbox.
// SPEC §6.6.
//
// The shape mirrors use.go: this file owns argv/env construction
// and returns a *RunInvocation; the CLI layer does the actual
// syscall.Exec. See use.go's package-level commentary for the
// rationale behind the split.
//
// Two behaviors are worth flagging here because they are not
// obvious from the spec text alone:
//
//   - The "DSN-already-set heuristic" (only matters when --no-dsn
//     is NOT set): we inject `-d <defaultDatabase>` UNLESS the
//     user already supplied a dbname themselves. SPEC §6.6 is
//     prescriptive about WHY (some tools, e.g. pgbench, take
//     dbname as a positional or as a libpq "dbname=..." conninfo
//     token, and double-supplying confuses them) but leaves the
//     detection to implementation. The heuristic we use is
//     deliberately simple — see dbnameSupplied below — and is
//     documented as "good enough for the common cases" with
//     known limits.
//
//   - --no-dsn affects argv ONLY. The PG* env vars are ALWAYS
//     injected because they're the sandbox's fallback contract:
//     even with no flags on argv, libpq-using tools should
//     connect to the right place via env. SPEC §6.6 spells this
//     out.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// RunOptions captures the inputs the CLI layer collects for
// `run`. It's a struct rather than a positional argv-tail list
// because we have a real flag (--no-dsn) plus a binary name plus
// the forwarded args; a single []string would force the CLI to
// thread parsing state we already know.
type RunOptions struct {
	// SandboxDir is the target sandbox.
	SandboxDir string

	// Binary is the PG utility name (e.g. "pg_dump", "pgbench").
	// Looked up via runner.Locate before exec — BinDir first,
	// then PATH.
	Binary string

	// ExtraArgs is the user's argv tail forwarded verbatim to
	// Binary. With NoDSN=false we MAY also prepend
	// -h/-p/-U/[-d] (see PrepareRun); with NoDSN=true ExtraArgs
	// is passed through untouched.
	ExtraArgs []string

	// NoDSN suppresses argv-side -h/-p/-U/-d injection. The PG*
	// env vars are still set regardless — those are the contract
	// the sandbox makes with libpq-using tools no matter what
	// argv shape the user chose.
	NoDSN bool
}

// RunInvocation is the argv + env description returned by
// PrepareRun. Same shape as UseInvocation; the two intentionally
// parallel each other because the CLI layer treats them the same
// way at the runner.Exec call site.
type RunInvocation struct {
	Binary string
	Args   []string
	Env    []string
}

// PrepareRun validates opts, loads the sandbox at opts.SandboxDir,
// and returns the *RunInvocation the CLI layer will hand to
// runner.Exec.
//
// Validation errors:
//   - opts.SandboxDir empty → ExitUsage.
//   - opts.Binary empty → ExitUsage.
//   - dir is not a sandbox → ExitNotASandbox.
//
// The actual `runner.Locate(opts.Binary)` call happens in the CLI
// layer (not here) because it requires a Runner with the
// sandbox's BinDir set, and the CLI is what constructs the
// runner. See LocateRunBinary below for the convenience helper.
func PrepareRun(ctx context.Context, opts RunOptions) (*RunInvocation, error) {
	_ = ctx // accepted for future-proofing; see use.go.

	if opts.SandboxDir == "" {
		return nil, wrapExit(ExitUsage, errors.New("sandbox.PrepareRun: SandboxDir is required"))
	}
	if opts.Binary == "" {
		return nil, wrapExit(ExitUsage, errors.New("sandbox.PrepareRun: Binary is required"))
	}

	cfg, err := loadSandboxOrFail(opts.SandboxDir)
	if err != nil {
		return nil, err
	}

	var args []string
	if opts.NoDSN {
		// --no-dsn: forwarded args go through untouched. The PG*
		// env is still injected (set below) so libpq-using tools
		// still connect to the right place.
		args = append(args, opts.ExtraArgs...)
	} else {
		// Always prepend -h/-p/-U. -d is conditional on the
		// dbname-already-set heuristic — see dbnameSupplied for
		// what we look for and the known limits.
		args = append(args,
			"-h", cfg.Host,
			"-p", strconv.Itoa(cfg.Port),
			"-U", cfg.Superuser,
		)
		if !dbnameSupplied(opts.ExtraArgs) {
			args = append(args, "-d", cfg.DefaultDatabase)
		}
		args = append(args, opts.ExtraArgs...)
	}

	return &RunInvocation{
		Binary: opts.Binary,
		Args:   args,
		Env:    pgConnEnv(cfg.Host, cfg.Port, cfg.Superuser, cfg.DefaultDatabase),
	}, nil
}

// dbnameSupplied scans the user's forwarded argv for any token
// that looks like a dbname specification. Returns true on the
// first match.
//
// Forms recognized (SPEC §6.6 implementation heuristic):
//
//	-d              (next arg is the dbname; we don't actually
//	                 need to look at the next arg — presence of
//	                 the flag is enough to suppress our injection)
//	-d<value>       libpq-short style, e.g. -dmydb
//	--dbname        (next arg is the dbname; same logic)
//	--dbname=<value>
//	dbname=<value>  positional conninfo token, common in pgbench
//
// Known limits / deliberate non-goals:
//
//   - We don't scan past `--`. Users rarely put both `--` and a
//     dbname-style token in the same argv and the simplification
//     keeps this short.
//   - We don't notice positional dbname args (e.g. `pg_dump
//     somedb`). Implementing this correctly would require
//     per-tool knowledge of which positional means dbname; SPEC
//     §6.6 explicitly accepts that limitation — users with
//     positional-dbname tools should pass `--no-dsn`.
//   - Token equality means `-d` matches but `-dmydb` matches via
//     the strings.HasPrefix branch; `-d` accidentally appearing
//     inside a longer flag (e.g. `--port=5432d`) is not
//     possible because we check for exact token equality and
//     prefix-with-=.
func dbnameSupplied(args []string) bool {
	for _, a := range args {
		switch {
		case a == "-d", a == "--dbname":
			return true
		case strings.HasPrefix(a, "-d") && len(a) > 2 && a[2] != '-':
			// `-dfoo` (libpq short form). Reject "-d-foo" which
			// isn't a real form and is more likely a typo than
			// an intentional dbname.
			return true
		case strings.HasPrefix(a, "--dbname="):
			return true
		case strings.HasPrefix(a, "dbname="):
			return true
		}
	}
	return false
}

// LocateRunBinary resolves opts.Binary via the runner's Locate.
// Exposed so the CLI layer can short-circuit with a clear "not
// found" error before calling runner.Exec.
func LocateRunBinary(runner pgexec.Runner, name string) (string, error) {
	if name == "" {
		return "", errors.New("sandbox.LocateRunBinary: name is required")
	}
	p, err := runner.Locate(name)
	if err != nil {
		return "", fmt.Errorf("sandbox.PrepareRun: locate %s: %w", name, err)
	}
	return p, nil
}
