// Command pg_sandbox is the CLI entry point for the Go port of the
// PostgreSQL sandbox tool. See ../../SPEC.md for the full functional
// specification.
//
// This file deliberately contains only the top-level subcommand
// dispatcher and the --version / --help bootstrap. Real command
// implementations live under internal/<domain>/ packages and are
// wired in here as they land. Keeping this file thin makes the
// dispatch logic easy to audit and lets each subcommand be developed
// in isolation.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// Build-stamped via -ldflags at link time. See Makefile.
//
// We deliberately use plain package-level vars (not constants)
// because the linker can only override vars, not consts, with
// `-X main.foo=bar`.
var (
	version = "0.0.0-dev"
	commit  = "unknown"
)

// subcommands enumerates every top-level command the binary will
// eventually accept. Each entry maps the user-typed name to a stub
// that prints a clear "not yet implemented" message and exits with
// the documented EXIT_GENERIC code (1). As real implementations
// land, the stub is replaced with a call into the appropriate
// internal/ package.
//
// Keeping the list here (rather than scattered through internal
// packages) means the help output, validation, and dispatcher all
// stay in sync from a single source of truth.
var subcommands = map[string]subcommand{
	// Single-instance lifecycle.
	"deploy":  {summary: "Create a new sandbox", run: notImplemented},
	"destroy": {summary: "Tear down a sandbox", run: notImplemented},
	"start":   {summary: "Start a sandbox's PostgreSQL instance", run: notImplemented},
	"stop":    {summary: "Stop a sandbox's PostgreSQL instance", run: notImplemented},
	"restart": {summary: "Restart a sandbox's PostgreSQL instance", run: notImplemented},
	"status":  {summary: "Report sandbox running/replication state", run: notImplemented},
	"use":     {summary: "Open psql against a sandbox", run: notImplemented},
	"run":     {summary: "Run any PG utility against a sandbox", run: notImplemented},
	"promote": {summary: "Promote a physical standby", run: notImplemented},

	// Configuration (replaces Python `setenv`; see SPEC §3 and §6.7).
	"config": {summary: "Inspect/mutate sandbox or global config", run: notImplemented},

	// Logical replication.
	"publish":   {summary: "Create a logical replication publication", run: notImplemented},
	"subscribe": {summary: "Create a logical replication subscription", run: notImplemented},

	// Cluster orchestration.
	"cluster": {summary: "Manage a named group of sandboxes (deploy/status/destroy)", run: notImplemented},

	// Cross-host listing and reports.
	"global_status": {summary: "List every sandbox on the host", run: notImplemented},
	"report":        {summary: "Generate a pg_gather HTML report", run: notImplemented},

	// Help is a real implementation even at this stage.
	"help": {summary: "Show help for a command", run: runHelp},
}

// subcommand bundles the metadata needed by the dispatcher: a short
// summary used in `pg_sandbox help`, and the function that actually
// runs the command. The run signature passes the already-stripped
// argv (without the program name and without the subcommand name)
// plus the writers to use, so subcommands are easy to test.
type subcommand struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

func main() {
	// We don't use the top-level `flag.Parse()` here because we
	// want to recognize --version and --help BEFORE deciding
	// whether the user typed a subcommand. Doing this by hand
	// keeps the dispatcher simple and avoids the standard library's
	// awkward "FlagSet ContinueOnError requires manual --help" dance.
	args := os.Args[1:]
	if len(args) == 0 {
		printTopHelp(os.Stderr)
		os.Exit(2) // EXIT_USAGE (see SPEC §8)
	}

	// Strip any leading global flags that we handle here. Anything
	// not matched falls through to the subcommand.
	for len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			fmt.Fprintf(os.Stdout, "pg_sandbox %s (commit %s, %s/%s, %s)\n",
				version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
			os.Exit(0)
		case "--help", "-h":
			printTopHelp(os.Stdout)
			os.Exit(0)
		default:
			// Not a known top-level-only flag; let the subcommand
			// dispatcher handle it.
			goto dispatch
		}
	}

dispatch:
	name := args[0]
	rest := args[1:]
	cmd, ok := subcommands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "pg_sandbox: unknown command %q\n", name)
		fmt.Fprintln(os.Stderr, "Run 'pg_sandbox help' to see available commands.")
		os.Exit(2) // EXIT_USAGE
	}
	os.Exit(cmd.run(rest, os.Stdout, os.Stderr))
}

// notImplemented is the temporary run-func for every subcommand whose
// real implementation has not yet landed. It prints a single, clear
// line to stderr and returns the generic error exit code so scripts
// can detect the situation.
func notImplemented(_ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "pg_sandbox: this command is not yet implemented in the Go port.")
	fmt.Fprintln(stderr, "See SPEC.md for the planned behavior, or use the Python pg_sandbox at the repo root.")
	return 1 // EXIT_GENERIC
}

// runHelp implements `pg_sandbox help [command]`. With no argument it
// prints the top-level command index; with a command name it prints
// the (currently brief) per-command summary. Per-command detailed
// help will be expanded as commands gain real implementations.
func runHelp(args []string, stdout, _ io.Writer) int {
	if len(args) == 0 {
		printTopHelp(stdout)
		return 0
	}
	name := args[0]
	cmd, ok := subcommands[name]
	if !ok {
		fmt.Fprintf(stdout, "pg_sandbox: no help available for %q\n", name)
		return 2
	}
	fmt.Fprintf(stdout, "pg_sandbox %s — %s\n", name, cmd.summary)
	fmt.Fprintln(stdout, "(Detailed flags TBD; see SPEC.md §6 for the planned behavior.)")
	return 0
}

// printTopHelp writes the top-level usage to the given writer. We
// build it in-line rather than via a template so there's zero
// dependency cost and the output is easy to grep for in tests later.
func printTopHelp(w io.Writer) {
	var b strings.Builder
	b.WriteString("pg_sandbox — manage local PostgreSQL sandbox instances\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  pg_sandbox [--version|--help] <command> [flags] [args]\n\n")
	b.WriteString("Commands:\n")
	// Print commands in a stable order rather than map order so the
	// help output diff-cleanly across runs.
	for _, name := range orderedCommandNames() {
		fmt.Fprintf(&b, "  %-14s %s\n", name, subcommands[name].summary)
	}
	b.WriteString("\nRun 'pg_sandbox help <command>' for more on a command.\n")
	b.WriteString("Full specification: go/SPEC.md\n")
	_, _ = io.WriteString(w, b.String())
}

// orderedCommandNames returns command names in the order we want
// users to discover them in `pg_sandbox help`. It deliberately
// groups by domain (lifecycle, then replication, then orchestration,
// then meta-commands) rather than alphabetically.
func orderedCommandNames() []string {
	return []string{
		"deploy", "destroy", "start", "stop", "restart", "status",
		"use", "run", "promote",
		"config",
		"publish", "subscribe",
		"cluster",
		"global_status", "report",
		"help",
	}
}
