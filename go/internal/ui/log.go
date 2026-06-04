// Structured logging for pg_sandbox built on the standard library's
// log/slog package. We deliberately do not pull in zap, zerolog, or
// any third-party logger — slog covers the leveled, structured,
// key/value needs documented in SPEC §4.6 with zero dependencies.
//
// Conventions:
//
//   - All diagnostic output goes to STDERR. STDOUT is reserved for
//     machine-consumable output (status --json, config get, etc.).
//
//   - The default human-friendly format is slog's TextHandler with a
//     small custom replacement on the time field so log lines stay
//     scannable in a terminal.
//
//   - Levels: debug < info < warn < error. The threshold is set by
//     NewLogger's argument; main.go resolves the threshold from
//     PGS_LOG_LEVEL, --debug, --quiet in that order.

package ui

import (
	"io"
	"log/slog"
	"strings"
)

// Level maps the string names accepted from PGS_LOG_LEVEL and the
// --log-level flag to slog.Level values. Unknown names are treated
// as a usage error by the caller (we return ok=false rather than
// silently picking a default — silent defaults are exactly the kind
// of hidden state SPEC §3.1.7 forbids).
func Level(name string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, true
	case "info", "":
		// Empty string maps to info because that's the documented
		// default. Treating "" as a valid input here lets callers
		// pass os.Getenv("PGS_LOG_LEVEL") through without first
		// checking for unset.
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error", "err":
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}

// NewLogger constructs a leveled logger that writes to w (typically
// os.Stderr). The handler is slog.TextHandler — human-friendly, one
// key=value pair per line. JSON output is intentionally not
// supported yet: nothing consumes our log stream programmatically
// today, and adding it later is one-line.
//
// The handler suppresses the time attribute by default. Sandbox
// commands run for seconds and write a handful of lines; the
// timestamp is noise. Callers that want timestamps can wrap the
// returned logger or use slog directly.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Drop the time field; the handler still emits the
			// rest of the line.
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(h)
}
