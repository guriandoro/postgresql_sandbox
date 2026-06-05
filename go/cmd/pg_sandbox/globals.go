// Global CLI flags wiring for pg_sandbox. SPEC §5.
//
// SPEC §5 declares --debug / --quiet / --color as flags that "MAY be
// supplied to any command (subcommand parser MUST accept them either
// before or after the subcommand name)". This file is the per-FlagSet
// helper every subcommand calls right after creating its FlagSet:
//
//	fs := flag.NewFlagSet("status", flag.ContinueOnError)
//	globals := registerGlobalFlags(fs)
//	... fs.StringVar(&otherFlag, …) ...
//	if err := fs.Parse(args); err != nil { return ui.ExitUsage.Int() }
//	logger, color, err := globals.Resolve(stderr)
//	if err != nil { … }
//
// The dispatcher in main.go ALSO sweeps these flags off the head of
// argv so `pg_sandbox --debug status` works identically to
// `pg_sandbox status --debug`. The sweep just re-prepends the captured
// tokens onto the subcommand's argv; the subcommand FlagSet then sees
// them in the position it expects.
//
// Color: the resolved ColorMode is propagated but NOT yet applied to
// output in this slice — no ANSI codes are emitted anywhere. Wiring
// color into the actual rendering layer is a later slice; for now we
// parse, validate, and hand the mode to whoever wants it.
//
// --debug / --quiet / --color before `help` are harmlessly ignored:
// the dispatcher intercepts `<cmd> --help` ahead of any global-flag
// processing, and runHelp itself doesn't take a FlagSet.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// GlobalOpts captures the parsed --debug / --quiet / --color values
// for one subcommand invocation. The zero value represents "no flags
// set" — log level defaults to Info, color to Auto.
type GlobalOpts struct {
	// Debug mirrors --debug. When set, log level drops to Debug and
	// pgexec.Runner emits a `# exec: …` line per child process.
	Debug bool

	// Quiet mirrors --quiet. When set, log level rises to Error and
	// the WrapStderr filter drops `level=INFO ` / `level=WARN `
	// prefixed lines that internal/sandbox code emits inline.
	Quiet bool

	// ColorMode is the resolved policy from --color=auto|always|never.
	// Default is ColorAuto (which renders as off unless stderr is a
	// TTY *and* NO_COLOR is unset — SPEC §4.6).
	ColorMode ui.ColorMode

	// rawColor is the literal flag value as parsed. Kept so Resolve
	// can surface the user-typed token in the usage error rather
	// than the normalized form, mirroring how Go's flag package
	// reports unknown values.
	rawColor string
}

// registerGlobalFlags wires --debug, --quiet, and --color onto fs and
// returns a struct whose fields are populated after fs.Parse runs.
// The returned pointer is owned by the caller for the lifetime of the
// subcommand; reuse across invocations is not supported.
func registerGlobalFlags(fs *flag.FlagSet) *GlobalOpts {
	o := &GlobalOpts{}
	fs.BoolVar(&o.Debug, "debug", false, "Verbose diagnostic logging and log every external command")
	fs.BoolVar(&o.Quiet, "quiet", false, "Suppress non-error diagnostic output")
	// SPEC §4.6 documents "color OFF by default; auto enables only
	// when stderr is TTY and NO_COLOR is unset". We use "auto" as
	// the flag default rather than the empty string so `--help`
	// listings show the documented default.
	fs.StringVar(&o.rawColor, "color", "auto", "ANSI color: auto|always|never")
	return o
}

// Resolve validates the parsed flags and turns them into a leveled
// slog.Logger plus a resolved ColorMode. Errors are usage errors —
// the caller should print them and return ui.ExitUsage.Int().
//
// Mutual exclusion: --debug and --quiet contradict; combining them
// is rejected rather than picking a precedence. SPEC §5's table lists
// both flags side by side without spelling out a winner, so refusing
// is the only honest reading.
func (o *GlobalOpts) Resolve(stderr io.Writer) (*slog.Logger, ui.ColorMode, error) {
	if o.Debug && o.Quiet {
		return nil, ui.ColorAuto, fmt.Errorf("pg_sandbox: --debug and --quiet are mutually exclusive")
	}
	mode, ok := ui.ParseColorMode(o.rawColor)
	if !ok {
		return nil, ui.ColorAuto, fmt.Errorf("pg_sandbox: invalid --color value %q (want auto|always|never)", o.rawColor)
	}
	o.ColorMode = mode

	level := slog.LevelInfo
	switch {
	case o.Debug:
		level = slog.LevelDebug
	case o.Quiet:
		level = slog.LevelError
	}
	return ui.NewLogger(stderr, level), mode, nil
}

// WrapStderr returns a writer that drops any line beginning with
// `level=INFO ` or `level=WARN ` when --quiet is set. SPEC §4.6 says
// quiet raises the threshold to error, but a chunk of pg_sandbox's
// existing diagnostic surface uses hand-formatted `fmt.Fprintf(w,
// "level=INFO msg=…")` writes (see internal/sandbox/* and cluster.go)
// rather than slog. Refactoring those to slog is a bigger slice; this
// filter is the bridge that makes --quiet honor SPEC §4.6 today
// without that refactor. ERROR lines and unprefixed lines pass
// through unchanged.
//
// When --quiet is not set, the original writer is returned unwrapped
// so there's zero overhead on the hot path.
func (o *GlobalOpts) WrapStderr(stderr io.Writer) io.Writer {
	if !o.Quiet {
		return stderr
	}
	return &quietFilter{inner: stderr, buf: &bytes.Buffer{}}
}

// quietFilter is the io.Writer that strips INFO/WARN level= lines.
// We buffer until we see a newline so we can decide per-line. Partial
// writes that don't contain a newline are held until the next Write
// completes the line (or until Flush, which the CLI doesn't call —
// every command's diagnostic output terminates with \n by convention).
type quietFilter struct {
	inner io.Writer
	buf   *bytes.Buffer
}

// Write satisfies io.Writer. It buffers p, then flushes any complete
// lines (decided by '\n'), dropping any that start with the gated
// level prefixes.
func (q *quietFilter) Write(p []byte) (int, error) {
	q.buf.Write(p)
	for {
		line, err := q.buf.ReadBytes('\n')
		if err != nil {
			// No more newline — put the partial back and stop.
			q.buf.Write(line)
			break
		}
		if !shouldDropQuietLine(line) {
			if _, werr := q.inner.Write(line); werr != nil {
				return len(p), werr
			}
		}
	}
	return len(p), nil
}

// shouldDropQuietLine returns true for lines that --quiet swallows.
// The rule is a prefix match on `level=INFO ` / `level=WARN ` —
// case-sensitive, with a trailing space to avoid matching
// `level=INFORMATIONAL` or other accidental neighbors. ERROR and
// DEBUG lines pass through (DEBUG only appears when --debug is on,
// which is mutually exclusive with --quiet anyway).
func shouldDropQuietLine(line []byte) bool {
	if bytes.HasPrefix(line, []byte("level=INFO ")) {
		return true
	}
	if bytes.HasPrefix(line, []byte("level=WARN ")) {
		return true
	}
	return false
}

// captureGlobalFlags walks the head of argv looking for --debug /
// --quiet / --color tokens (in both `--color=v` and `--color v`
// shapes; both `--flag` and `-flag` single-dash variants because Go's
// flag package treats them identically) and returns them as a slice
// plus the remaining tail. The dispatcher uses this BEFORE looking up
// the subcommand name so the user-typed token order doesn't matter:
//
//	pg_sandbox --debug status -s X       ⇒ status -s X --debug (effectively)
//	pg_sandbox --color always status …   ⇒ status … --color always
//
// We deliberately stop at the first non-recognized token so the
// subcommand-name token never gets consumed. Unknown flags continue
// to be the subcommand parser's problem — we don't pre-validate here.
func captureGlobalFlags(argv []string) (captured, rest []string) {
	i := 0
	for i < len(argv) {
		tok := argv[i]
		switch tok {
		case "--debug", "-debug", "--quiet", "-quiet":
			captured = append(captured, tok)
			i++
			continue
		case "--color", "-color":
			// `--color VALUE` two-token form. If there's no next
			// token, leave it to the subcommand FlagSet to error.
			if i+1 >= len(argv) {
				captured = append(captured, tok)
				i++
				continue
			}
			captured = append(captured, tok, argv[i+1])
			i += 2
			continue
		}
		// Single-token `--color=VALUE` / `-color=VALUE` form.
		if hasPrefix(tok, "--color=") || hasPrefix(tok, "-color=") {
			captured = append(captured, tok)
			i++
			continue
		}
		break
	}
	return captured, argv[i:]
}

// hasPrefix is strings.HasPrefix inlined to keep this file's import
// set tiny. The pgexec/argv files use strings extensively; here we
// don't want to pull it in just for one helper.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

