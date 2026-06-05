// CLI wiring for `pg_sandbox destroy`. SPEC §6.3.
//
// Confirmation lives in this layer (not in the sandbox package) so
// the CLI owns prompt I/O. The TTY check uses the stdlib pattern of
// stat'ing os.Stdin and looking for ModeCharDevice — this avoids
// pulling in golang.org/x/term and keeps us in the stdlib-only
// policy.

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func runDestroy(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)
	var (
		sandboxDir string
		force      bool
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.BoolVar(&force, "force", false, "Skip confirmation prompt")
	fs.BoolVar(&force, "f", false, "Alias for --force")
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
		fmt.Fprintln(stderr, "pg_sandbox destroy: --sandbox-dir is required")
		usageHint(stderr, "destroy")
		return ui.ExitUsage.Int()
	}
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox destroy: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox destroy: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	if !force {
		// SPEC §4.7: non-TTY without --force is a refusal, not a
		// silent proceed and not a silent abort.
		if !stdinIsTTY() {
			fmt.Fprintln(stderr, "pg_sandbox destroy: stdin is not a TTY and --force was not set; refusing")
			return ui.ExitNotATTY.Int()
		}
		if !confirmDestroy(cfg.Name, sandboxDir, os.Stdin, stderr) {
			fmt.Fprintln(stderr, "pg_sandbox destroy: aborted")
			return ui.ExitOK.Int()
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
	err = sandbox.Destroy(ctx, runner, sandbox.DestroyOptions{SandboxDir: sandboxDir}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox destroy: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	return ui.ExitOK.Int()
}

// destroyHelp prints `pg_sandbox help destroy`. SPEC §6.3.
func destroyHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox destroy — tear down a sandbox")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox destroy -s <dir> [--force]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Stops the cluster (if running) and removes the sandbox directory tree.")
	fmt.Fprintln(w, "Without --force, prompts y/N on stderr; refuses if stdin is not a TTY.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
		{"-f, --force", "Skip the confirmation prompt"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.3.")
}

// stdinIsTTY reports whether os.Stdin is connected to a terminal.
// Stdlib-only: we check the Mode flags. ModeCharDevice is set on a
// stdin connected to a TTY (canonical pattern; see e.g. mattn/go-
// isatty's source). Pipes and redirected files lack it.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// confirmDestroy prompts on stderr and reads a single line from r.
// "y" or "yes" (case-insensitive, leading/trailing whitespace
// stripped) is consent. Anything else, including empty input, is
// refusal — SPEC §4.7 specifies y/N where N is the default.
func confirmDestroy(name, dir string, r io.Reader, stderr io.Writer) bool {
	fmt.Fprintf(stderr, "destroy sandbox %q at %s? [y/N]: ", name, dir)
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes"
}
