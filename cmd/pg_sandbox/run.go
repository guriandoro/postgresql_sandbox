// CLI wiring for `pg_sandbox run`. SPEC §6.6.
//
// `run` is an exec-style command (see use.go's commentary for the
// exec semantics). The extra wrinkle versus `use` is that the
// binary to exec is itself a positional argument: the user types
// `pg_sandbox run -s X -- pg_dump -t mytable`, and we exec
// pg_dump after auto-injecting -h/-p/-U/[-d] (unless --no-dsn was
// passed).
//
// Flag parsing details:
//
//   - The Go flag package stops at `--` and `fs.Args()` returns
//     everything after. The first element of that slice is the
//     binary name; the rest is forwarded verbatim. Missing binary
//     name is ExitUsage.
//
//   - --no-dsn (alias -n) suppresses argv-side DSN injection but
//     not env injection. SPEC §6.6 spells this out.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

func runRun(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var (
		sandboxDir string
		noDSN      bool
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&noDSN, "no-dsn", false, "Skip argv-side -h/-p/-U/-d injection (env still set)")
	fs.BoolVar(&noDSN, "n", false, "Alias for --no-dsn")
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
		fmt.Fprintln(stderr, "pg_sandbox run: --sandbox-dir is required")
		usageHint(stderr, "run")
		return ui.ExitUsage.Int()
	}

	// fs.Args() yields the positional tail: binary name first,
	// then its argv.
	tail := fs.Args()
	if len(tail) == 0 {
		fmt.Fprintln(stderr, "pg_sandbox run: missing binary name (usage: run -s X -- <binary> [args...])")
		usageHint(stderr, "run")
		return ui.ExitUsage.Int()
	}
	binary := tail[0]
	forwarded := tail[1:]

	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox run: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox run: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	invoke, err := sandbox.PrepareRun(context.Background(), sandbox.RunOptions{
		SandboxDir: sandboxDir,
		Binary:     binary,
		ExtraArgs:  forwarded,
		NoDSN:      noDSN,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox run: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}

	// Locate the user-requested binary up front so a typo yields
	// a clean "not found in BinDir/PATH" line. There's no
	// dedicated exit code for "binary not found" in SPEC §8;
	// ExitGeneric matches the spec's notes for `run` (the docs
	// admit this is a small gap to revisit if it becomes
	// painful).
	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
	if _, err := sandbox.LocateRunBinary(runner, invoke.Binary); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox run: %v\n", err)
		return ui.ExitGeneric.Int()
	}

	// Hand the PG* env to the runner so syscall.Exec inherits
	// them on top of os.Environ.
	runner.Env = invoke.Env
	if err := runner.Exec(invoke.Binary, invoke.Args...); err != nil {
		// syscall.Exec returned: exec failed before the new image
		// took over.
		fmt.Fprintf(stderr, "pg_sandbox run: exec %s: %v\n", invoke.Binary, err)
		return ui.ExitGeneric.Int()
	}
	// Unreachable on success.
	return ui.ExitOK.Int()
}

// runRunHelp prints `pg_sandbox help run`. SPEC §6.6. The function
// is named runRunHelp rather than runHelp because runHelp is the
// dispatcher entry for `pg_sandbox help` itself, defined in main.go.
func runRunHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox run — run any PG utility against a sandbox")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox run -s <dir> [--no-dsn] -- <binary> [args...]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "execs <binary> with PG* env (host, port, user, dbname) sourced from the")
	fmt.Fprintln(w, "sandbox config. By default the same values are also injected on the argv as")
	fmt.Fprintln(w, "-h/-p/-U/-d (so e.g. pg_dump picks them up even when it ignores env); pass")
	fmt.Fprintln(w, "--no-dsn to skip the argv injection while keeping env. <binary> is looked up")
	fmt.Fprintln(w, "in the sandbox's bin/ dir first, then PATH.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
		{"-n, --no-dsn", "Skip argv-side -h/-p/-U/-d injection (env still set)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  pg_sandbox run -s mybox -- pg_dump -t mytable")
	fmt.Fprintln(w, "  pg_sandbox run -s mybox --no-dsn -- vacuumdb --all")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.6.")
}
