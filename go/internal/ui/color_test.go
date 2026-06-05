// Tests for the color-policy primitives. StderrIsTTY isn't unit-
// tested here: it depends on the real os.Stderr file descriptor and
// the harness's TTY state changes between `go test` invocations,
// which makes any assertion either trivially true or trivially false.
// The helper is wired into globals.Resolve which is itself exercised
// end-to-end in cmd/pg_sandbox/globals_test.go.

package ui

import "testing"

func TestParseColorMode_validAndInvalid(t *testing.T) {
	cases := []struct {
		in     string
		want   ColorMode
		wantOK bool
	}{
		{"auto", ColorAuto, true},
		{"AUTO", ColorAuto, true},
		{" Auto ", ColorAuto, true},
		{"", ColorAuto, true}, // documented: empty = auto
		{"always", ColorAlways, true},
		{"never", ColorNever, true},
		{"potato", ColorAuto, false}, // unknown → not ok
		{"on", ColorAuto, false},
		{"off", ColorAuto, false},
	}
	for _, tc := range cases {
		got, ok := ParseColorMode(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("ParseColorMode(%q) = (%v, %v); want (%v, %v)",
				tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestShouldUseColor_table(t *testing.T) {
	cases := []struct {
		name     string
		mode     ColorMode
		isTTY    bool
		noColor  string
		want     bool
	}{
		{"never with everything else true",
			ColorNever, true, "", false},
		{"always overrides NO_COLOR",
			ColorAlways, false, "1", true},
		{"always overrides non-TTY",
			ColorAlways, false, "", true},
		{"auto and TTY and NO_COLOR unset → on",
			ColorAuto, true, "", true},
		{"auto but not TTY → off",
			ColorAuto, false, "", false},
		{"auto and TTY but NO_COLOR set → off",
			ColorAuto, true, "1", false},
		{"auto and TTY but NO_COLOR=empty-string still considered unset",
			ColorAuto, true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldUseColor(tc.mode, tc.isTTY, tc.noColor)
			if got != tc.want {
				t.Errorf("ShouldUseColor(%v,%v,%q) = %v; want %v",
					tc.mode, tc.isTTY, tc.noColor, got, tc.want)
			}
		})
	}
}
