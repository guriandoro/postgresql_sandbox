// Unit tests for the argv pre-processing helpers. See argv.go.

package main

import (
	"reflect"
	"testing"
)

func TestReorderBoolFlags_promotesTrailingFlag(t *testing.T) {
	// Happy path: a single bool flag after a single positional must
	// be moved to the front so flag.FlagSet.Parse sees it.
	got := reorderBoolFlags(
		[]string{"18.3", "--force"},
		[]string{"--force", "-f"},
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
		[]string{"--force", "-f"},
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
		[]string{"--force", "-f"},
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
		[]string{"--force", "-f"},
	)
	want := []string{"18.3", "--", "--force"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_emptyInputs(t *testing.T) {
	// Boundary: empty argv and empty knownBoolFlags must be safe.
	if got := reorderBoolFlags(nil, []string{"--force"}); len(got) != 0 {
		t.Errorf("nil argv: got %v, want empty", got)
	}
	got := reorderBoolFlags([]string{"a", "b"}, nil)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nil knownBoolFlags: got %v, want %v", got, want)
	}
}
