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
//     NewLogger's argument; the dispatcher resolves it from --debug,
//     --quiet, and the PGS_DEBUG env-var alias (see SPEC §4.9 and
//     globals.go::Resolve).

package ui

import (
	"io"
	"log/slog"
)

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
