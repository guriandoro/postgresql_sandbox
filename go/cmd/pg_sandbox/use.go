// CLI wiring for `pg_sandbox use`. SPEC §6.5.
//
// `use` is an exec-style command: on success the current Go
// process is replaced by psql via syscall.Exec, so signals,
// readline state, exit code, and TTY ownership all behave as if
// the user ran psql directly. The runner.Exec path therefore
// "never returns on success" — anything after that call only runs
// when exec itself failed.
//
// We deliberately locate psql FIRST (via runner.Locate) before
// calling exec. A missing psql surfacing through syscall.Exec
// would produce a confusing kernel-level error; locating up front
// gives the user a clear "psql not found in BinDir/PATH" line.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func runUse(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var sandboxDir string
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if sandboxDir == "" {
		fmt.Fprintln(stderr, "pg_sandbox use: --sandbox-dir is required")
		usageHint(stderr, "use")
		return ui.ExitUsage.Int()
	}

	// Anything after our flags (whether separated by `--` or just
	// trailing the parsed flags) is forwarded verbatim to psql.
	// Go's flag package stops at `--` so fs.Args() is already
	// the right slice.
	forwarded := fs.Args()

	// Per-CLI belt-and-braces: refuse a non-sandbox dir before
	// even loading the config. The sandbox package re-checks; the
	// double check guarantees a clean ExitNotASandbox even if the
	// package surface later changes.
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox use: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox use: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	invoke, err := sandbox.PrepareUse(context.Background(), sandboxDir, forwarded)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox use: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}

	// Locate the binary up front so a missing psql gives a clean
	// error before we call into syscall.Exec.
	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
	if _, err := sandbox.LocateUseBinary(runner); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox use: %v\n", err)
		return ui.ExitPsqlFailed.Int()
	}

	// Set the PG* env on the runner so syscall.Exec inherits them.
	// pgexec.Exec appends e.Env on top of os.Environ() so the
	// user's locale, PATH, etc. are preserved.
	runner.Env = invoke.Env
	if err := runner.Exec(invoke.Binary, invoke.Args...); err != nil {
		// syscall.Exec returned an error → exec actually failed
		// (e.g. permission denied). On success the call doesn't
		// return; we can only reach here in the failure branch.
		fmt.Fprintf(stderr, "pg_sandbox use: exec psql: %v\n", err)
		return ui.ExitPsqlFailed.Int()
	}
	// Unreachable on success — the child psql owns the process
	// now. We return 0 only to satisfy the function's int
	// signature; this line never actually executes.
	return ui.ExitOK.Int()
}

// useHelp prints `pg_sandbox help use`. SPEC §6.5.
func useHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox use — open psql against a sandbox")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox use -s <dir> [-- <psql args>...]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "execs psql with PG* env (host, port, user, dbname) sourced from the sandbox")
	fmt.Fprintln(w, "config so the current process is replaced by psql — signals, TTY, exit code")
	fmt.Fprintln(w, "all behave as if you ran psql directly. Anything after `--` is forwarded.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  pg_sandbox use -s mybox")
	fmt.Fprintln(w, "  pg_sandbox use -s mybox -- -c 'select version()'")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.5.")
}
