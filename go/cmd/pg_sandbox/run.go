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

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func runRun(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
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
	runner := pgexec.New(cfg.BinDir)
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
