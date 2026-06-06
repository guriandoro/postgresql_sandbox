// Tests for the top-level dispatcher's help / version paths and for
// runHelp (the `pg_sandbox help [<cmd>]` handler in main.go).
//
// These cover the surface that is hard to reach from the per-handler
// _test.go files because it lives ABOVE the run* functions: --help /
// -h / --version short-circuits, the unknown-command branch, the
// "global flags only, no subcommand" branch, the `<cmd> --help`
// interception, and the `help <cmd>` lookup.
//
// We exercise the dispatch seam (extracted from main() in main.go) so
// the test can pass writers and observe both stdout and stderr.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func TestDispatch_emptyArgsPrintsTopHelpToStderr(t *testing.T) {
	// `pg_sandbox` with no args is a usage error: top help to stderr,
	// exit 2 (ExitUsage). Mirrors getopt convention.
	var stdout, stderr bytes.Buffer
	rc := dispatch(nil, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d (ExitUsage)", rc, ui.ExitUsage.Int())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on no-args, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "pg_sandbox") {
		t.Errorf("stderr missing top-level usage banner: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Commands:") {
		t.Errorf("stderr missing Commands: header: %q", stderr.String())
	}
}

func TestDispatch_topLevelHelpShortCircuitsToStdout(t *testing.T) {
	// `pg_sandbox --help` and `-h` exit 0 with the top banner on
	// stdout — distinguishing user-asked-for-help (exit 0) from
	// missed-the-target (exit 2).
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := dispatch([]string{flag}, &stdout, &stderr)
			if rc != ui.ExitOK.Int() {
				t.Errorf("rc = %d, want %d (ExitOK)", rc, ui.ExitOK.Int())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr should be empty on --help, got %q", stderr.String())
			}
			if !strings.Contains(stdout.String(), "pg_sandbox") {
				t.Errorf("stdout missing top banner: %q", stdout.String())
			}
		})
	}
}

func TestDispatch_versionFlagShortCircuits(t *testing.T) {
	// --version / -V: print one line to stdout, exit 0. The line MUST
	// include the version string the linker stamps (or the dev
	// default).
	for _, flag := range []string{"--version", "-V"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := dispatch([]string{flag}, &stdout, &stderr)
			if rc != ui.ExitOK.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitOK.Int())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr should be empty on --version, got %q", stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), "pg_sandbox ") {
				t.Errorf("stdout doesn't start with 'pg_sandbox ': %q", stdout.String())
			}
			if !strings.Contains(stdout.String(), "commit") {
				t.Errorf("stdout missing 'commit' tag: %q", stdout.String())
			}
		})
	}
}

func TestDispatch_unknownCommandIsExitUsage(t *testing.T) {
	// An unknown top-level command surfaces as ExitUsage with the
	// user-typed token in the error. We don't want the dispatcher to
	// silently fall through to anything else.
	var stdout, stderr bytes.Buffer
	rc := dispatch([]string{"not-a-real-command"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr missing 'unknown command' marker: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not-a-real-command") {
		t.Errorf("stderr doesn't surface the bad token: %q", stderr.String())
	}
}

func TestDispatch_globalFlagsOnlyNoSubcommandIsUsage(t *testing.T) {
	// User typed `pg_sandbox --debug` (or `--color always`) with no
	// subcommand. Dispatcher must surface a top-help-on-stderr / exit
	// 2 result, NOT silently exit 0 from "I ate every token" or
	// crash from a nil-index lookup.
	cases := [][]string{
		{"--debug"},
		{"--quiet"},
		{"--color", "always"},
		{"--color=auto"},
		{"--debug", "--color=never"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := dispatch(args, &stdout, &stderr)
			if rc != ui.ExitUsage.Int() {
				t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
			}
			if !strings.Contains(stderr.String(), "pg_sandbox") {
				t.Errorf("stderr missing top help: %q", stderr.String())
			}
		})
	}
}

func TestDispatch_subcommandHelpRendersRichBlockOnStdout(t *testing.T) {
	// `pg_sandbox <cmd> --help` / `-h` MUST hit the dispatcher's
	// rich-help interception path (rc=0, stdout has the per-command
	// usage block) rather than dropping through to the FlagSet's
	// terse default-usage (rc=2 on stderr).
	//
	// We sample every top-level command that has a `help` printer so
	// the table-driven check catches a future contributor wiring a
	// new command into subcommands{} without a help printer.
	cmds := []string{
		"deploy", "destroy", "start", "stop", "restart", "status",
		"use", "run", "promote",
		"config",
		"publish", "subscribe",
		"cluster",
		"global_status", "report",
		"build", "cleanup-install-versions",
		"help",
	}
	for _, name := range cmds {
		t.Run(name+" --help", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := dispatch([]string{name, "--help"}, &stdout, &stderr)
			if rc != ui.ExitOK.Int() {
				t.Errorf("rc = %d, want %d; stderr=%q", rc, ui.ExitOK.Int(), stderr.String())
			}
			// The rich block goes to stdout. The standard suffix in
			// every per-command help is "pg_sandbox <name>" near the
			// top, but we just sanity-check that something landed on
			// stdout AND not on stderr — the exact wording per cmd is
			// out of scope here.
			if stdout.Len() == 0 {
				t.Errorf("stdout empty on `%s --help`", name)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr should be empty on `%s --help`, got %q", name, stderr.String())
			}
		})
		t.Run(name+" -h", func(t *testing.T) {
			// -h is the documented alias; same path as --help.
			var stdout, stderr bytes.Buffer
			rc := dispatch([]string{name, "-h"}, &stdout, &stderr)
			if rc != ui.ExitOK.Int() {
				t.Errorf("rc = %d, want %d; stderr=%q", rc, ui.ExitOK.Int(), stderr.String())
			}
			if stdout.Len() == 0 {
				t.Errorf("stdout empty on `%s -h`", name)
			}
		})
	}
}

func TestRunHelp_noArgsPrintsTopHelp(t *testing.T) {
	// `pg_sandbox help` with no command name lists every top-level
	// command to stdout, exit 0. This is the discovery surface.
	var stdout, stderr bytes.Buffer
	rc := runHelp(nil, &stdout, &stderr)
	if rc != ui.ExitOK.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitOK.Int())
	}
	out := stdout.String()
	if !strings.Contains(out, "Commands:") {
		t.Errorf("stdout missing Commands: header: %q", out)
	}
	// Spot-check that a representative spread of commands is listed
	// — protects against a future map-iteration regression.
	for _, want := range []string{"deploy", "destroy", "status", "config", "cluster", "build"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing command %q in top index: %q", want, out)
		}
	}
}

func TestRunHelp_knownCommandRendersRichBlock(t *testing.T) {
	// `pg_sandbox help status` reaches the same rich help renderer as
	// `pg_sandbox status --help`. This pins the equivalence the help
	// docs promise.
	var stdout, stderr bytes.Buffer
	rc := runHelp([]string{"status"}, &stdout, &stderr)
	if rc != ui.ExitOK.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitOK.Int())
	}
	out := stdout.String()
	if !strings.Contains(out, "pg_sandbox status") {
		t.Errorf("stdout missing 'pg_sandbox status' banner: %q", out)
	}
	if !strings.Contains(out, "sandbox-dir") {
		t.Errorf("stdout doesn't mention sandbox-dir flag: %q", out)
	}
}

func TestRunHelp_unknownCommandIsConfigKeyUnknownStyle(t *testing.T) {
	// Asking for help on a non-existent command goes to stdout with
	// a "no help available" line and returns ExitUsage. The stdout
	// channel is deliberate — `help` is the discovery surface and
	// the user is in the middle of typing exploratory tokens; we
	// don't want this line buried on stderr.
	var stdout, stderr bytes.Buffer
	rc := runHelp([]string{"not-a-real-command"}, &stdout, &stderr)
	if rc != ui.ExitUsage.Int() {
		t.Errorf("rc = %d, want %d", rc, ui.ExitUsage.Int())
	}
	if !strings.Contains(stdout.String(), "no help available") {
		t.Errorf("stdout missing 'no help available': %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "not-a-real-command") {
		t.Errorf("stdout doesn't echo the bad name: %q", stdout.String())
	}
}

func TestPrintTopHelp_listsEveryRegisteredCommand(t *testing.T) {
	// printTopHelp walks orderedCommandNames() and looks up each in
	// subcommands{}. A future contributor who registers a new
	// command but forgets to extend orderedCommandNames would silently
	// drop it from `pg_sandbox help`'s index. We check the union by
	// pinning every key in subcommands appears in the rendered output.
	var buf bytes.Buffer
	printTopHelp(&buf)
	out := buf.String()
	for name := range subcommands {
		if !strings.Contains(out, name) {
			t.Errorf("printTopHelp omits command %q (registered in subcommands but not in orderedCommandNames?)", name)
		}
	}
}
