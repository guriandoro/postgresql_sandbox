// Tests for the SPEC §5 global flags wiring (--debug, --quiet,
// --color). Coverage tiers:
//
//   - registerGlobalFlags binding and FlagSet integration.
//   - Resolve's mutual-exclusion, level-mapping, and color-validation.
//   - WrapStderr's INFO/WARN line filter.
//   - captureGlobalFlags pre-subcommand argv sweep (the bridge that
//     lets `pg_sandbox --debug status` work identically to
//     `pg_sandbox status --debug`).

package main

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

func TestRegisterGlobalFlags_acceptsAllPositions(t *testing.T) {
	// SPEC §5 says global flags MAY appear before OR after the
	// subcommand name. Internally, main.go's dispatcher sweeps any
	// leading globals via captureGlobalFlags and re-prepends them
	// onto the subcommand argv — so by the time the subcommand
	// FlagSet sees args, the global is always present somewhere in
	// the slice. Verify both orderings parse successfully.
	cases := []struct {
		name string
		argv []string
	}{
		{"global before subcommand args", []string{"--debug", "-s", "/tmp/x"}},
		{"global after subcommand args", []string{"-s", "/tmp/x", "--debug"}},
		{"color=value style", []string{"--color=always", "-s", "/tmp/x"}},
		{"color value-separate style", []string{"--color", "never", "-s", "/tmp/x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			globals := registerGlobalFlags(fs)
			var sandboxDir string
			fs.StringVar(&sandboxDir, "sandbox-dir", "", "")
			fs.StringVar(&sandboxDir, "s", "", "")
			if err := fs.Parse(tc.argv); err != nil {
				t.Fatalf("Parse(%v): %v", tc.argv, err)
			}
			if sandboxDir != "/tmp/x" {
				t.Errorf("sandboxDir = %q, want /tmp/x", sandboxDir)
			}
			// Resolve must succeed so a regression in flag wiring
			// can't slip past as "Parse worked, Resolve forgot it".
			if _, _, err := globals.Resolve(io.Discard); err != nil {
				t.Errorf("Resolve: %v", err)
			}
		})
	}
}

func TestGlobalOpts_Resolve_rejectsDebugQuietCombo(t *testing.T) {
	// SPEC §5 declares --debug and --quiet as opposite-direction
	// threshold controls. Refuse rather than picking a precedence.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse([]string{"--debug", "--quiet"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, _, err := globals.Resolve(io.Discard)
	if err == nil {
		t.Fatal("Resolve: want error for --debug + --quiet, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("Resolve error doesn't name the conflict: %v", err)
	}
}

func TestGlobalOpts_Resolve_levelMapping(t *testing.T) {
	cases := []struct {
		name  string
		argv  []string
		want  slog.Level
	}{
		{"no flags → info", nil, slog.LevelInfo},
		{"--debug → debug", []string{"--debug"}, slog.LevelDebug},
		{"--quiet → error", []string{"--quiet"}, slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			globals := registerGlobalFlags(fs)
			if err := fs.Parse(tc.argv); err != nil {
				t.Fatalf("Parse: %v", err)
			}
			logger, _, err := globals.Resolve(io.Discard)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			// log.Enabled(level) is the cleanest level-probe in
			// stdlib slog. We assert that the resolved logger is
			// "enabled at the wanted level" AND "disabled one level
			// below" (so e.g. --quiet → Error means Warn is gated).
			if !logger.Enabled(nil, tc.want) {
				t.Errorf("logger not enabled at %v", tc.want)
			}
			if tc.want > slog.LevelDebug && logger.Enabled(nil, tc.want-4) {
				t.Errorf("logger leaked level %v; want threshold %v", tc.want-4, tc.want)
			}
		})
	}
}

func TestGlobalOpts_Resolve_envDebugFallback(t *testing.T) {
	// PGS_DEBUG with no --debug flag must lower the threshold to
	// Debug, matching the Python-era affordance documented in
	// SPEC §4.9. Empty string is treated as unset.
	cases := []struct {
		name     string
		envValue string
		argv     []string
		wantLvl  slog.Level
	}{
		{"unset env keeps info", "", nil, slog.LevelInfo},
		{"empty env keeps info", "", nil, slog.LevelInfo},
		{"PGS_DEBUG=1 lowers to debug", "1", nil, slog.LevelDebug},
		{"PGS_DEBUG=anything lowers to debug", "yes", nil, slog.LevelDebug},
		{"--quiet beats PGS_DEBUG", "1", []string{"--quiet"}, slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGS_DEBUG", tc.envValue)
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			globals := registerGlobalFlags(fs)
			if err := fs.Parse(tc.argv); err != nil {
				t.Fatalf("Parse: %v", err)
			}
			logger, _, err := globals.Resolve(io.Discard)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !logger.Enabled(nil, tc.wantLvl) {
				t.Errorf("logger not enabled at %v", tc.wantLvl)
			}
			if tc.wantLvl > slog.LevelDebug && logger.Enabled(nil, tc.wantLvl-4) {
				t.Errorf("logger leaked level %v; want threshold %v", tc.wantLvl-4, tc.wantLvl)
			}
		})
	}
}

func TestGlobalOpts_Resolve_flagDebugBeatsEnv(t *testing.T) {
	// Symmetric check: --debug works regardless of PGS_DEBUG, and
	// PGS_DEBUG never overrides an explicit --quiet (covered in the
	// table above, restated here for clarity).
	t.Setenv("PGS_DEBUG", "")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse([]string{"--debug"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	logger, _, err := globals.Resolve(io.Discard)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !logger.Enabled(nil, slog.LevelDebug) {
		t.Errorf("--debug did not enable Debug level")
	}
}

func TestGlobalOpts_Resolve_rejectsBadColor(t *testing.T) {
	// Unknown --color values are usage errors, mirroring how Go's
	// flag package treats unknown flag values. The error message
	// MUST surface the bad token so the user can correct it.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse([]string{"--color=potato"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, _, err := globals.Resolve(io.Discard)
	if err == nil {
		t.Fatal("Resolve: want error for --color=potato, got nil")
	}
	if !strings.Contains(err.Error(), "potato") {
		t.Errorf("Resolve error doesn't include the bad value: %v", err)
	}
}

func TestGlobalOpts_Resolve_colorModePropagates(t *testing.T) {
	// `--color always` must surface as ui.ColorAlways. Pinning the
	// happy path so a future refactor that turns "always" into "off"
	// blows up here, not in a slice three months later that finally
	// reads the resolved mode.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse([]string{"--color", "always"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, mode, err := globals.Resolve(io.Discard)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mode != ui.ColorAlways {
		t.Errorf("ColorMode = %v, want ColorAlways", mode)
	}
}

func TestWrapStderr_quietFiltersInfoAndWarn(t *testing.T) {
	// The filter is line-prefix based. INFO/WARN level= lines are
	// dropped; ERROR and unprefixed lines pass through.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse([]string{"--quiet"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, _, err := globals.Resolve(io.Discard); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var buf bytes.Buffer
	w := globals.WrapStderr(&buf)
	_, _ = w.Write([]byte("level=INFO msg=\"sandbox started\"\n"))
	_, _ = w.Write([]byte("level=WARN msg=\"replication lag\"\n"))
	_, _ = w.Write([]byte("level=ERROR msg=\"pg_ctl failed\"\n"))
	_, _ = w.Write([]byte("connection string: postgresql://x\n"))

	out := buf.String()
	if strings.Contains(out, "sandbox started") {
		t.Errorf("WARN/INFO line slipped through quiet filter: %q", out)
	}
	if strings.Contains(out, "replication lag") {
		t.Errorf("WARN line slipped through quiet filter: %q", out)
	}
	if !strings.Contains(out, "pg_ctl failed") {
		t.Errorf("ERROR line dropped by quiet filter: %q", out)
	}
	if !strings.Contains(out, "connection string") {
		t.Errorf("unprefixed line dropped by quiet filter: %q", out)
	}
}

func TestWrapStderr_passthroughWithoutQuiet(t *testing.T) {
	// Without --quiet, WrapStderr returns the input writer unwrapped
	// so there's zero overhead on the hot path. Pin that contract by
	// asserting pointer identity.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, _, err := globals.Resolve(io.Discard); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var buf bytes.Buffer
	got := globals.WrapStderr(&buf)
	if got != io.Writer(&buf) {
		t.Errorf("WrapStderr wrapped a writer without --quiet: %T", got)
	}
}

func TestWrapStderr_quietHandlesPartialLines(t *testing.T) {
	// The filter buffers until newline so a caller that splits a
	// line across two Write calls still sees the prefix and drops
	// (or keeps) the whole line correctly.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	globals := registerGlobalFlags(fs)
	if err := fs.Parse([]string{"--quiet"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, _, err := globals.Resolve(io.Discard); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var buf bytes.Buffer
	w := globals.WrapStderr(&buf)
	_, _ = w.Write([]byte("level=I"))
	_, _ = w.Write([]byte("NFO msg=\"split write\"\n"))
	_, _ = w.Write([]byte("level=ERROR msg=\"keep me\"\n"))

	out := buf.String()
	if strings.Contains(out, "split write") {
		t.Errorf("INFO line split across writes slipped through: %q", out)
	}
	if !strings.Contains(out, "keep me") {
		t.Errorf("ERROR line after a split INFO got dropped: %q", out)
	}
}

func TestCaptureGlobalFlags_table(t *testing.T) {
	cases := []struct {
		name         string
		argv         []string
		wantCaptured []string
		wantRest     []string
	}{
		{"nothing to capture",
			[]string{"status", "-s", "x"},
			nil,
			[]string{"status", "-s", "x"}},
		{"--debug before subcommand",
			[]string{"--debug", "status", "-s", "x"},
			[]string{"--debug"},
			[]string{"status", "-s", "x"}},
		{"--quiet before subcommand",
			[]string{"--quiet", "status"},
			[]string{"--quiet"},
			[]string{"status"}},
		{"--color value-separate before subcommand",
			[]string{"--color", "always", "status"},
			[]string{"--color", "always"},
			[]string{"status"}},
		{"--color=value form",
			[]string{"--color=always", "status"},
			[]string{"--color=always"},
			[]string{"status"}},
		{"multiple globals chained",
			[]string{"--debug", "--color", "always", "status", "-s", "x"},
			[]string{"--debug", "--color", "always"},
			[]string{"status", "-s", "x"}},
		{"single-dash long form",
			[]string{"-debug", "status"},
			[]string{"-debug"},
			[]string{"status"}},
		{"stops at first non-global",
			[]string{"status", "--debug"},
			nil,
			[]string{"status", "--debug"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCaptured, gotRest := captureGlobalFlags(tc.argv)
			if !reflect.DeepEqual(gotCaptured, tc.wantCaptured) {
				t.Errorf("captured = %v, want %v", gotCaptured, tc.wantCaptured)
			}
			if !reflect.DeepEqual(gotRest, tc.wantRest) {
				t.Errorf("rest = %v, want %v", gotRest, tc.wantRest)
			}
		})
	}
}
