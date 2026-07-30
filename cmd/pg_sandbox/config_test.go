// Tests for config CLI argv parsing: scope flags (-s/--global) after
// KEY=VALUE or KEY positionals must parse via parseSubcommandArgs.

package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func newConfigSetFlagSet() (*flag.FlagSet, *scope) {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	registerGlobalFlags(fs)
	var sc scope
	parseScopeFlags(fs, &sc)
	return fs, &sc
}

func newConfigGetFlagSet() (*flag.FlagSet, *scope) {
	fs := flag.NewFlagSet("config get", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	registerGlobalFlags(fs)
	var sc scope
	parseScopeFlags(fs, &sc)
	return fs, &sc
}

func TestParseSubcommandArgs_configSet_scopeAfterPairs(t *testing.T) {
	fs, sc := newConfigSetFlagSet()
	if err := parseSubcommandArgs(fs, []string{"host=0.0.0.0", "port=5433", "-s", "pg18"}); err != nil {
		t.Fatalf("parseSubcommandArgs: %v", err)
	}
	if sc.sandboxDir != "pg18" {
		t.Errorf("sandboxDir = %q, want %q", sc.sandboxDir, "pg18")
	}
	if sc.global {
		t.Error("global should be false")
	}
	rest := fs.Args()
	want := []string{"host=0.0.0.0", "port=5433"}
	if len(rest) != len(want) || rest[0] != want[0] || rest[1] != want[1] {
		t.Errorf("fs.Args() = %v, want %v", rest, want)
	}

	fs2, sc2 := newConfigSetFlagSet()
	if err := parseSubcommandArgs(fs2, []string{"host=127.0.0.1", "--global"}); err != nil {
		t.Fatalf("parseSubcommandArgs: %v", err)
	}
	if !sc2.global {
		t.Error("global = false, want true")
	}
	if sc2.sandboxDir != "" {
		t.Errorf("sandboxDir = %q, want empty", sc2.sandboxDir)
	}
	rest2 := fs2.Args()
	if len(rest2) != 1 || rest2[0] != "host=127.0.0.1" {
		t.Errorf("fs.Args() = %v, want [host=127.0.0.1]", rest2)
	}
}

func TestParseSubcommandArgs_configGet_keyBeforeScope(t *testing.T) {
	fs, sc := newConfigGetFlagSet()
	if err := parseSubcommandArgs(fs, []string{"port", "-s", "pg18"}); err != nil {
		t.Fatalf("parseSubcommandArgs: %v", err)
	}
	if sc.sandboxDir != "pg18" {
		t.Errorf("sandboxDir = %q, want %q", sc.sandboxDir, "pg18")
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] != "port" {
		t.Errorf("fs.Args() = %v, want [port]", rest)
	}
}

func TestRunConfigSet_trailingScopeNotUsageError(t *testing.T) {
	var stderr bytes.Buffer
	rc := runConfigSet([]string{"host=0.0.0.0", "port=5433", "-s", "/nonexistent"}, nil, &stderr)
	_ = rc
	if strings.Contains(stderr.String(), "one of --sandbox-dir/-s or --global is required") {
		t.Errorf("trailing -s misparsed as positional; stderr=%q", stderr.String())
	}
}

func TestRunConfigGet_trailingScopeNotUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runConfigGet([]string{"port", "-s", "/nonexistent"}, &stdout, &stderr)
	_ = rc
	if strings.Contains(stderr.String(), "one of --sandbox-dir/-s or --global is required") {
		t.Errorf("trailing -s misparsed; stderr=%q", stderr.String())
	}
	if strings.Contains(stderr.String(), "exactly one KEY argument is required") {
		t.Errorf("trailing -s counted as extra KEY; stderr=%q", stderr.String())
	}
}
