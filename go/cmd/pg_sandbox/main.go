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
		"deploy":  {summary: "Create a new sandbox", run: runDeploy, help: deployHelp},
		"destroy": {summary: "Tear down a sandbox", run: runDestroy, help: destroyHelp},
		"start":   {summary: "Start a sandbox's PostgreSQL instance", run: runStart, help: startHelp},
		"stop":    {summary: "Stop a sandbox's PostgreSQL instance", run: runStop, help: stopHelp},
		"restart": {summary: "Restart a sandbox's PostgreSQL instance", run: runRestart, help: restartHelp},
		"status":  {summary: "Report sandbox running/replication state", run: runStatus, help: statusHelp},
		"use":     {summary: "Open psql against a sandbox", run: runUse, help: useHelp},
		"run":     {summary: "Run any PG utility against a sandbox", run: runRun, help: runRunHelp},
		"promote": {summary: "Promote a physical standby", run: runPromote, help: promoteHelp},

		// Configuration (replaces Python `setenv`; see SPEC §3 and §6.7).
		"config": {summary: "Inspect/mutate sandbox or global config", run: runConfig, help: printConfigUsage},

		// Logical replication.
		"publish":   {summary: "Create a logical replication publication", run: runPublish, help: publishHelp},
		"subscribe": {summary: "Create a logical replication subscription", run: runSubscribe, help: subscribeHelp},

		// Cluster orchestration.
		"cluster": {summary: "Manage a named group of sandboxes (deploy/status/destroy)", run: runCluster, help: printClusterUsage},

		// Cross-host listing and reports.
		"global_status": {summary: "List every sandbox on the host", run: runGlobalStatus, help: globalStatusHelp},
		"report":        {summary: "Generate a pg_gather HTML report", run: runReport, help: reportHelp},

		// Phase 2: source compilation + install pruning.
		"build":                    {summary: "Compile a PostgreSQL version from source", run: runBuild, help: buildHelp},
		"cleanup-install-versions": {summary: "Prune unused PostgreSQL install dirs", run: runCleanupInstallVersions, help: cleanupInstallVersionsHelp},

		// Help is a real implementation even at this stage.
		"help": {summary: "Show help for a command", run: runHelp, help: helpHelp},
	}
}

// subcommand bundles the metadata needed by the dispatcher: a short
// summary used in `pg_sandbox help`, the function that actually runs
// the command, and a per-command help printer used by both
// `pg_sandbox help <cmd>` and the dispatcher's `<cmd> --help` hook.
// The run signature passes the already-stripped argv (without the
// program name and without the subcommand name) plus the writers to
// use, so subcommands are easy to test. `help` writes the detailed
// usage/flags block to the given writer; nil falls back to the brief
// summary line.
type subcommand struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
	help    func(w io.Writer)
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
	// Intercept `<cmd> --help` / `<cmd> -h` so it always renders the
	// rich help on stdout and exits 0, rather than dropping into the
	// flag package's terser default usage on stderr.
	//
	// We only check the FIRST positional after the subcommand name —
	// that's the canonical placement for --help and avoids second-
	// guessing argv shapes that the subcommand's FlagSet handles
	// (e.g. `cluster deploy --help`, which the inner dispatcher
	// already maps to a rich block).
	if len(rest) > 0 && (rest[0] == "--help" || rest[0] == "-h") && cmd.help != nil {
		cmd.help(os.Stdout)
		os.Exit(ui.ExitOK.Int())
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
// prints the top-level command index; with a command name it calls
// that command's detailed `help` printer. Commands without a help
// printer fall back to the short summary line (kept as a safety net
// so a missing wiring doesn't silently crash).
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
	if cmd.help != nil {
		cmd.help(stdout)
		return ui.ExitOK.Int()
	}
	fmt.Fprintf(stdout, "pg_sandbox %s — %s\n", name, cmd.summary)
	return ui.ExitOK.Int()
}

// helpFlag describes one flag row in a `pg_sandbox help <cmd>` block.
// names is the left-column text (e.g. "-s, --sandbox-dir <dir>") and
// desc is the right-column description. Authoring is by hand rather
// than reflecting over a real flag.FlagSet because the help text
// merges the short-alias and long-form rows into a single line that
// the flag package doesn't model.
type helpFlag struct {
	names string
	desc  string
}

// writeHelpFlags renders a list of helpFlag rows with the left column
// padded to the widest names string. Used by every per-command help
// printer so flag tables across commands look uniform.
func writeHelpFlags(w io.Writer, flags []helpFlag) {
	width := 0
	for _, f := range flags {
		if l := len(f.names); l > width {
			width = l
		}
	}
	for _, f := range flags {
		fmt.Fprintf(w, "  %-*s  %s\n", width, f.names, f.desc)
	}
}

// helpHelp prints `pg_sandbox help help`. Useful as a discovery aid
// for `pg_sandbox help <tab>`-style completion users — they should
// learn that `--help` and `help <cmd>` are equivalent.
func helpHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox help — show help for a command")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox help [<command>]")
	fmt.Fprintln(w, "  pg_sandbox <command> --help")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "With no argument, lists every top-level command. With a command name,")
	fmt.Fprintln(w, "prints that command's usage, flags, and notes.")
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
	// help output diff-cleanly across runs. Column width is computed
	// from the widest command name so longer names (e.g.
	// "cleanup-install-versions") don't bust the layout — the prior
	// hardcoded 14-char width misaligned that row.
	names := orderedCommandNames()
	width := 0
	for _, n := range names {
		if l := len(n); l > width {
			width = l
		}
	}
	for _, name := range names {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, name, subcommands[name].summary)
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
