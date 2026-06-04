// Unit tests for the argv pre-processing helpers. See argv.go.

package main

import (
	"flag"
	"reflect"
	"sort"
	"testing"
)

func TestReorderBoolFlags_promotesTrailingFlag(t *testing.T) {
	// Happy path: a single bool flag after a single positional must
	// be moved to the front so flag.FlagSet.Parse sees it.
	got := reorderBoolFlags(
		[]string{"18.3", "--force"},
		[]string{"force", "f"},
	)
	want := []string{"--force", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_mixedPositionalAndFlag(t *testing.T) {
	// Flag sandwiched between two positionals; both positionals must
	// keep their relative order and follow the flag.
	got := reorderBoolFlags(
		[]string{"18.3", "-f", "16.5"},
		[]string{"force", "f"},
	)
	want := []string{"-f", "18.3", "16.5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_noopWhenAlreadyOrdered(t *testing.T) {
	// When the flag is already before the positional, the result is
	// identical (no spurious reshuffling).
	got := reorderBoolFlags(
		[]string{"--force", "18.3"},
		[]string{"force", "f"},
	)
	want := []string{"--force", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_dashDashStopsProcessing(t *testing.T) {
	// `--` is the verbatim end-of-options marker. Anything after it
	// must NOT be promoted, even if it would otherwise match a known
	// bool flag.
	got := reorderBoolFlags(
		[]string{"18.3", "--", "--force"},
		[]string{"force", "f"},
	)
	want := []string{"18.3", "--", "--force"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_emptyInputs(t *testing.T) {
	// Boundary: empty argv and empty knownBoolFlags must be safe.
	if got := reorderBoolFlags(nil, []string{"force"}); len(got) != 0 {
		t.Errorf("nil argv: got %v, want empty", got)
	}
	got := reorderBoolFlags([]string{"a", "b"}, nil)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nil knownBoolFlags: got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_promotesEqualsValueForm(t *testing.T) {
	// `--force=true` after a positional must be promoted as-is; Go's
	// flag package accepts the `=value` form for bool flags.
	got := reorderBoolFlags(
		[]string{"18.3", "--force=true"},
		[]string{"force", "f"},
	)
	want := []string{"--force=true", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_promotesSingleDashLongForm(t *testing.T) {
	// Go's flag package treats `-force` and `--force` identically.
	// The single-dash long form must be promoted just like `--force`.
	got := reorderBoolFlags(
		[]string{"18.3", "-force"},
		[]string{"force", "f"},
	)
	want := []string{"-force", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_promotesShortEqualsFalse(t *testing.T) {
	// `-f=false` after a positional must be promoted as-is. The
	// original token (with dashes and `=value`) is what Parse needs
	// to see.
	got := reorderBoolFlags(
		[]string{"18.3", "-f=false"},
		[]string{"force", "f"},
	)
	want := []string{"-f=false", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_doesNotPromoteDifferentName(t *testing.T) {
	// `--forces` is a different name; it must NOT be promoted just
	// because it shares a prefix with `force`.
	got := reorderBoolFlags(
		[]string{"18.3", "--forces"},
		[]string{"force", "f"},
	)
	want := []string{"18.3", "--forces"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_doesNotPromoteValueTakingFlag(t *testing.T) {
	// `--root` is a value-taking flag whose bare name isn't in the
	// bool list, so it must NOT be promoted. (Promoting it would
	// separate it from its value and break parsing.)
	got := reorderBoolFlags(
		[]string{"18.3", "--root", "/tmp/sb"},
		[]string{"force", "f"},
	)
	want := []string{"18.3", "--root", "/tmp/sb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBoolFlagNames(t *testing.T) {
	// boolFlagNames must return exactly the bare names of every
	// BoolVar-registered flag on the FlagSet, and must skip flags
	// that take a value (StringVar, IntVar, etc.). Order doesn't
	// matter — reorderBoolFlags treats the slice as a set — so we
	// compare after sorting.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var (
		force   bool
		f       bool
		root    string
		binDir  string
		retries int
	)
	fs.BoolVar(&force, "force", false, "")
	fs.BoolVar(&f, "f", false, "")
	fs.StringVar(&root, "root", "", "")
	fs.StringVar(&binDir, "bin-dir", "", "")
	fs.IntVar(&retries, "retries", 0, "")

	got := boolFlagNames(fs)
	want := []string{"f", "force"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
