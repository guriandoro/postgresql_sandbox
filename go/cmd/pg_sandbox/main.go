// Command pg_sandbox is the CLI entry point for the Go port of the
// PostgreSQL sandbox tool. See ../../SPEC.md for the full functional
// specification.
//
// This file deliberately contains only the top-level subcommand
// dispatcher and the --version / --help bootstrap. Each subcommand's
// CLI wiring lives next to it in cmd/pg_sandbox/<cmd>.go and delegates
// to the relevant internal/<domain>/ package for the actual work.
// Keeping this file thin makes the dispatch logic easy to audit and
// lets each subcommand be developed in isolation.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
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

// subcommands enumerates every top-level command the binary accepts.
// Each entry maps the user-typed name to its real handler, which
// lives next to this file (one `cmd/pg_sandbox/<cmd>.go` per
// command); `help` is the exception, handled inline below.
//
// Keeping the list here (rather than scattered through internal
// packages) means the help output, validation, and dispatcher all
// stay in sync from a single source of truth.
//
// The map is populated in init() rather than as a var initializer
// because `runHelp` reads back from `subcommands` to render help
// for a named command — a static var-initializer cycle the Go
// compiler refuses to compile. Populating from init() defers the
// reference until after all package-level identifiers exist.
var subcommands map[string]subcommand

func init() {
	subcommands = map[string]subcommand{
		// Single-instance lifecycle.
		"deploy":  {summary: "Create a new sandbox", run: runDeploy},
		"destroy": {summary: "Tear down a sandbox", run: runDestroy},
		"start":   {summary: "Start a sandbox's PostgreSQL instance", run: runStart},
		"stop":    {summary: "Stop a sandbox's PostgreSQL instance", run: runStop},
		"restart": {summary: "Restart a sandbox's PostgreSQL instance", run: runRestart},
		"status":  {summary: "Report sandbox running/replication state", run: runStatus},
		"use":     {summary: "Open psql against a sandbox", run: runUse},
		"run":     {summary: "Run any PG utility against a sandbox", run: runRun},
		"promote": {summary: "Promote a physical standby", run: runPromote},

		// Configuration (replaces Python `setenv`; see SPEC §3 and §6.7).
		"config": {summary: "Inspect/mutate sandbox or global config", run: runConfig},

		// Logical replication.
		"publish":   {summary: "Create a logical replication publication", run: runPublish},
		"subscribe": {summary: "Create a logical replication subscription", run: runSubscribe},

		// Cluster orchestration.
		"cluster": {summary: "Manage a named group of sandboxes (deploy/status/destroy)", run: runCluster},

		// Cross-host listing and reports.
		"global_status": {summary: "List every sandbox on the host", run: runGlobalStatus},
		"report":        {summary: "Generate a pg_gather HTML report", run: runReport},

		// Phase 2: source compilation + install pruning.
		"build":                    {summary: "Compile a PostgreSQL version from source", run: runBuild},
		"cleanup-install-versions": {summary: "Prune unused PostgreSQL install dirs", run: runCleanupInstallVersions},

		// Help is a real implementation even at this stage.
		"help": {summary: "Show help for a command", run: runHelp},
	}
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
		os.Exit(ui.ExitUsage.Int())
	}

	// Strip any leading global flags that we handle here. Anything
	// not matched falls through to the subcommand.
	for len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			fmt.Fprintf(os.Stdout, "pg_sandbox %s (commit %s, %s/%s, %s)\n",
				version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
			os.Exit(ui.ExitOK.Int())
		case "--help", "-h":
			printTopHelp(os.Stdout)
			os.Exit(ui.ExitOK.Int())
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
		os.Exit(ui.ExitUsage.Int())
	}
	os.Exit(cmd.run(rest, os.Stdout, os.Stderr))
}

// usageHint prints a one-line pointer at `pg_sandbox help <cmd>`. It
// is the friendly replacement for `flag.FlagSet.Usage()` on the
// missing-required-argument branch — flag.Usage dumps the entire
// flag listing (often 20+ lines) which buries the actual error. The
// hint keeps the failure message short and discoverable.
//
// `cmd` should be the top-level subcommand name as it appears in
// the dispatcher (e.g. "deploy", "cluster"), NOT the inner sub-
// subcommand like "cluster deploy" — runHelp can only resolve names
// from the top-level subcommands map.
func usageHint(w io.Writer, cmd string) {
	fmt.Fprintf(w, "Run 'pg_sandbox help %s' for usage.\n", cmd)
}

// runHelp implements `pg_sandbox help [command]`. With no argument it
// prints the top-level command index; with a command name it prints
// the (currently brief) per-command summary. Per-command detailed
// flag help is still TBD — for now we point readers at SPEC.md §6.
func runHelp(args []string, stdout, _ io.Writer) int {
	if len(args) == 0 {
		printTopHelp(stdout)
		return ui.ExitOK.Int()
	}
	name := args[0]
	cmd, ok := subcommands[name]
	if !ok {
		fmt.Fprintf(stdout, "pg_sandbox: no help available for %q\n", name)
		return ui.ExitUsage.Int()
	}
	fmt.Fprintf(stdout, "pg_sandbox %s — %s\n", name, cmd.summary)
	fmt.Fprintln(stdout, "(Detailed flags TBD; see SPEC.md §6 for the planned behavior.)")
	return ui.ExitOK.Int()
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
		"build", "cleanup-install-versions",
		"help",
	}
}
