// Color-policy primitives for pg_sandbox.
//
// SPEC §4.6 says color is OFF by default, opt-in via
// `--color=auto|always|never`. `auto` enables color only when stderr
// is a TTY *and* NO_COLOR is unset (NO_COLOR being the de-facto
// cross-tool convention; https://no-color.org).
//
// The primitives here are deliberately small and side-effect-free.
// CLI glue (registerGlobalFlags in cmd/pg_sandbox/globals.go) is the
// caller that combines them; the `ui` package itself never reads the
// environment or stats descriptors on import — that's a deliberate
// non-goal so tests can drive the helpers with synthetic inputs.
//
// Color is NOT yet emitted anywhere in pg_sandbox: this slice parses
// and propagates the resolved mode, but ANSI emission is deferred to
// a later slice. See globals.go for the matching comment.

package ui

import (
	"os"
	"strings"
)

// ColorMode is the resolved color policy for one CLI run.
type ColorMode int

const (
	// ColorAuto is the documented default — color on only if the
	// destination looks interactive and NO_COLOR isn't set. The
	// concrete decision is delegated to ShouldUseColor so callers
	// can test it without poking at globals.
	ColorAuto ColorMode = iota

	// ColorAlways forces color on regardless of TTY / NO_COLOR.
	// Useful for piping into pagers that understand ANSI.
	ColorAlways

	// ColorNever forces color off. Useful in CI and when redirecting
	// stderr to a log file that doesn't render escapes.
	ColorNever
)

// ParseColorMode maps the user-typed `--color` value to a ColorMode.
// Recognized: "auto", "always", "never" (case-insensitive). The empty
// string maps to ColorAuto so callers can pass `os.Getenv("…")` or a
// FlagSet default of "auto" through without first checking for unset.
// Unknown values return ok=false; the caller surfaces that as a usage
// error (mirrors Level's contract — no silent defaults for typos).
func ParseColorMode(s string) (ColorMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ColorAuto, true
	case "always":
		return ColorAlways, true
	case "never":
		return ColorNever, true
	}
	return ColorAuto, false
}

// StderrIsTTY reports whether os.Stderr is connected to a terminal.
// Stdlib-only via the ModeCharDevice flag — the same idiom destroy.go's
// stdinIsTTY uses for SPEC §4.7's confirmation gate. Pipes and
// redirected files lack the flag and so report false.
func StderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ShouldUseColor decides whether to emit ANSI escapes, per SPEC §4.6:
//
//   - ColorNever  → always false.
//   - ColorAlways → always true.
//   - ColorAuto   → true only if isTTY && noColorEnv == "".
//
// The function is pure; the caller passes the TTY result and the
// NO_COLOR env value so this stays testable without poking at globals.
// (Note: the NO_COLOR convention treats *any* non-empty value as "no
// color"; an explicit empty string does not disable color.)
func ShouldUseColor(mode ColorMode, isTTY bool, noColorEnv string) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	// ColorAuto.
	return isTTY && noColorEnv == ""
}
