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

	"github.com/guriandoro/postgresql_sandbox/internal/ui"
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
	// main is the only place we read os.Args / os.Std{out,err} /
	// call os.Exit. Everything dispatch-related is delegated to
	// dispatch so cmd-package tests can drive both global-flag
	// orderings end-to-end without spawning a subprocess.
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch is the testable seam under main. Given the program argv
// (sans argv[0]) and the writers to use for output, it does the
// top-level --version / --help / global-flag sweep / subcommand
// lookup and returns the exit code its caller should hand to
// os.Exit. Extracting this from main lets globals_dispatch_test.go
// exercise the "global flag before OR after the subcommand name"
// surface against a real run* handler without spawning a binary.
func dispatch(args []string, stdout, stderr io.Writer) int {
	// We don't use the top-level `flag.Parse()` here because we
	// want to recognize --version and --help BEFORE deciding
	// whether the user typed a subcommand. Doing this by hand
	// keeps the dispatcher simple and avoids the standard library's
	// awkward "FlagSet ContinueOnError requires manual --help" dance.
	if len(args) == 0 {
		printTopHelp(stderr)
		return ui.ExitUsage.Int()
	}

	// Strip any leading global flags that we handle here. --version /
	// --help short-circuit immediately; --debug / --quiet / --color
	// are SPEC §5 global flags (accepted before OR after the
	// subcommand name) — we capture and re-prepend them so the
	// subcommand's FlagSet sees them in the position it expects.
	// Anything else falls through to the subcommand.
	var leadingGlobals []string
	for len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			fmt.Fprintf(stdout, "pg_sandbox %s (commit %s, %s/%s, %s)\n",
				version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
			return ui.ExitOK.Int()
		case "--help", "-h":
			printTopHelp(stdout)
			return ui.ExitOK.Int()
		}
		// Pull --debug / --quiet / --color off the head, then
		// continue the loop in case more global flags follow before
		// the subcommand name. captureGlobalFlags stops at the first
		// non-matching token (which is typically the subcommand
		// name), so we use len(after)<len(args) as the "we ate at
		// least one token" sentinel.
		captured, after := captureGlobalFlags(args)
		if len(captured) == 0 {
			break
		}
		leadingGlobals = append(leadingGlobals, captured...)
		args = after
	}

	if len(args) == 0 {
		// User typed only global flags, no subcommand. Mirror the
		// no-arg branch above.
		printTopHelp(stderr)
		return ui.ExitUsage.Int()
	}

	name := args[0]
	rest := args[1:]
	cmd, ok := subcommands[name]
	if !ok {
		fmt.Fprintf(stderr, "pg_sandbox: unknown command %q\n", name)
		fmt.Fprintln(stderr, "Run 'pg_sandbox help' to see available commands.")
		return ui.ExitUsage.Int()
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
		cmd.help(stdout)
		return ui.ExitOK.Int()
	}
	// Re-prepend any global flags we swept off the head so the
	// subcommand's FlagSet sees them in the position it expects.
	// `help` is special — runHelp doesn't take global flags, so we
	// just drop the captured tokens silently when dispatching to it.
	if len(leadingGlobals) > 0 && name != "help" {
		rest = append(append([]string{}, leadingGlobals...), rest...)
	}
	return cmd.run(rest, stdout, stderr)
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
