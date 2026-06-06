// External binary execution for pg_sandbox.
//
// SPEC §4.8 says the tool shells out to a fixed set of PostgreSQL
// utilities: initdb, pg_ctl, psql, pg_basebackup, pg_dump,
// pg_config (plus make/configure/tar/curl in Phase 2). This
// package centralizes that work behind a small Runner interface so:
//
//   - Commands construct argv slices declaratively and hand them to
//     a Runner — they never touch os/exec directly. This keeps the
//     security-sensitive subprocess code in one auditable file.
//
//   - Tests substitute a Fake Runner and assert on what would have
//     been executed. No real binary launches, no real PG required,
//     unit tests stay fast and hermetic.
//
//   - Binary resolution is centralized: lookup in BinDir (which the
//     user pointed at via --bin-dir) before falling back to PATH.
//     The Locate method exposes this so callers can short-circuit
//     if a binary is missing before running anything else.
//
// Three execution modes are exposed because pg_sandbox legitimately
// needs all three:
//
//   - Run / RunWithStdin — capture stdout/stderr (for psql -c
//     queries, status checks). Returns Result.
//
//   - RunInteractive — child gets the user's stdin/stdout/stderr
//     directly (no capture). Used by the `run` subcommand for tools
//     like pgbench whose output the user wants to watch live.
//
//   - Exec (via os.Executable replace semantics) — actually
//     replaces the current process with the child. Used by `use`
//     so psql owns the TTY and exit code with no wrapping.

package pgexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// debugExecWriter is where the `# exec: ...` debug line goes. We
// publish it as a package var (default os.Stderr) so tests can swap
// in a bytes.Buffer without bringing up a real subprocess pipeline.
// SPEC §4.6 requires the line on stderr; production callers should
// not change this.
var debugExecWriter io.Writer = os.Stderr

// Result is what Run-family methods return. ExitCode is always
// populated (it's 0 on success, the child's exit code on a clean
// non-zero exit, -1 if Err is set before the process even started).
// Stdout/Stderr are populated for Run/RunWithStdin; empty for
// RunInteractive (the child wrote directly to the user's TTY).
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

// Runner is the abstraction every pg_sandbox command depends on for
// invoking external PG binaries. The concrete *Exec implementation
// runs them via os/exec; the *Fake implementation in fake.go
// captures calls without running anything.
type Runner interface {
	// Run executes name with args and returns the captured output.
	// The context's cancellation kills the child.
	Run(ctx context.Context, name string, args ...string) Result

	// RunWithStdin is Run but with stdin piped from r.
	RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) Result

	// RunInteractive connects the child to the parent process's
	// own stdin/stdout/stderr. Used when the user is supposed to
	// watch live output (pgbench, etc.).
	RunInteractive(ctx context.Context, name string, args ...string) Result

	// Exec replaces the current process with the child via
	// syscall.Exec. Returns only on error; on success the
	// current process is gone and this function never returns.
	// Used by the `use` subcommand so psql owns the TTY.
	Exec(name string, args ...string) error

	// Locate resolves a binary name to its absolute path. It
	// looks in BinDir first (if set), then PATH. Used by callers
	// that want a "does this exist" check before running.
	Locate(name string) (string, error)
}

// Exec is the real Runner implementation. Zero-value is usable;
// fields are all optional knobs.
type Exec struct {
	// BinDir is the directory checked first for every binary name
	// passed to Run/Locate. If empty, Exec falls back to PATH only.
	// Typical value: the sandbox's resolved bin-dir (e.g.
	// "/opt/postgresql/18.4/bin").
	BinDir string

	// Env is appended to the parent's environment when spawning
	// children. Use it to set PG* variables (PGHOST, PGPORT, etc.)
	// for child processes invoked via `run` or `use`.
	Env []string

	// Logger, if non-nil, receives one debug-level log line per
	// invocation with the full argv before the process starts.
	// SPEC §4.6 says debug output is prefixed `# exec:` — that's
	// the log message used here.
	Logger *slog.Logger
}

// New constructs an Exec with the given BinDir. It exists as a
// convenience so most callers can write `pgexec.New(cfg.BinDir)`
// instead of building the struct literal.
func New(binDir string) *Exec { return &Exec{BinDir: binDir} }

// Locate implements Runner.Locate. The lookup order is:
//
//  1. If name contains a path separator, treat it as an explicit
//     path. Use it as-is if it exists.
//  2. If BinDir is set, stat it first. A missing or non-directory
//     BinDir surfaces with a focused error rather than being
//     buried inside a "<name> not found in BinDir or PATH" wrap.
//     Then look for the binary in two spots:
//       a. BinDir/name (SPEC: BinDir IS the bin/ directory).
//       b. BinDir/bin/name (UX: users naturally pass the install
//          prefix, e.g. /opt/postgresql/18.3, expecting the tool
//          to find bin/initdb underneath).
//  3. Fall back to exec.LookPath (which scans PATH).
//
// Returns the absolute path on success. On failure the error names
// the actual problem (bin-dir missing, binary not in bin-dir,
// nothing in PATH) without wrapping it under the underlying
// os/exec error string.
func (e *Exec) Locate(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		// User gave us an explicit path. Trust it but check it
		// exists — failing here gives a better error than failing
		// inside os/exec.Start.
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("pgexec.Locate: %s: %w", name, err)
		}
		return filepath.Clean(name), nil
	}
	if e.BinDir != "" {
		// Stat BinDir first so a typo or wrong-version path (e.g.
		// /opt/postgresql/18.4 when the install on disk is 18.3)
		// surfaces as "bin-dir does not exist" instead of the more
		// confusing "<binary> not found in BinDir … or PATH: exec:
		// …" double-wrap users used to see.
		st, statErr := os.Stat(e.BinDir)
		switch {
		case os.IsNotExist(statErr):
			return "", fmt.Errorf("bin-dir does not exist: %s", e.BinDir)
		case statErr != nil:
			return "", fmt.Errorf("bin-dir %s: %w", e.BinDir, statErr)
		case !st.IsDir():
			return "", fmt.Errorf("bin-dir is not a directory: %s", e.BinDir)
		}
		// Prefer BinDir/name (matches SPEC's "BinDir is bin/")
		// but also accept BinDir/bin/name so users can hand us
		// the install prefix (/opt/postgresql/18.3) without
		// remembering to append /bin.
		if cand := filepath.Join(e.BinDir, name); isExecutable(cand) {
			return cand, nil
		}
		if cand := filepath.Join(e.BinDir, "bin", name); isExecutable(cand) {
			return cand, nil
		}
		// Neither layout matched. Try PATH; on failure, frame the
		// error around the bin-dir since that's what the user
		// configured.
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%s not found in bin-dir %s or its bin/ subdir (and not in PATH)", name, e.BinDir)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH (no --bin-dir / PGS_BIN_DIR set)", name)
	}
	return p, nil
}

// isExecutable reports whether path looks runnable: it exists, is
// not a directory, and has at least one execute bit set. We do not
// distinguish owner/group/other — Stat doesn't give us a portable
// way to ask "by *me*", and the worst case is os/exec.Start later
// returns EACCES with a clearer error.
func isExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	if st.IsDir() {
		return false
	}
	return st.Mode()&0111 != 0
}

// Run implements Runner.Run.
func (e *Exec) Run(ctx context.Context, name string, args ...string) Result {
	return e.runCaptured(ctx, nil, name, args)
}

// RunWithStdin implements Runner.RunWithStdin.
func (e *Exec) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) Result {
	return e.runCaptured(ctx, stdin, name, args)
}

// runCaptured is the shared implementation of Run and RunWithStdin.
// The two public methods are thin wrappers so the stdin parameter
// is explicit at the call site (preventing accidental nil-stdin
// surprises) without duplicating the body.
func (e *Exec) runCaptured(ctx context.Context, stdin io.Reader, name string, args []string) Result {
	full, err := e.Locate(name)
	if err != nil {
		return Result{ExitCode: -1, Err: err}
	}
	e.logExec(full, args)

	cmd := exec.CommandContext(ctx, full, args...)
	if len(e.Env) > 0 {
		cmd.Env = append(os.Environ(), e.Env...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	runErr := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	res.ExitCode, res.Err = exitCodeOf(runErr)
	return res
}

// RunInteractive implements Runner.RunInteractive.
func (e *Exec) RunInteractive(ctx context.Context, name string, args ...string) Result {
	full, err := e.Locate(name)
	if err != nil {
		return Result{ExitCode: -1, Err: err}
	}
	e.logExec(full, args)

	cmd := exec.CommandContext(ctx, full, args...)
	if len(e.Env) > 0 {
		cmd.Env = append(os.Environ(), e.Env...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	res := Result{}
	res.ExitCode, res.Err = exitCodeOf(runErr)
	return res
}

// Exec implements Runner.Exec. It calls syscall.Exec, which replaces
// the current process image with the child. On success this function
// does not return. The current process's PID, file descriptors, and
// TTY all become the child's, which is exactly the semantics `use`
// wants: the user's signals, exit code, and readline state all act
// on psql with no wrapper visible.
//
// Note we propagate e.Env on top of os.Environ rather than calling
// syscall.Exec with a pristine env — the parent already established
// the user's locale, PATH, etc., and clobbering all of that would
// surprise the user.
func (e *Exec) Exec(name string, args ...string) error {
	full, err := e.Locate(name)
	if err != nil {
		return err
	}
	e.logExec(full, args)

	// argv[0] in the child is the path it was launched as. Some
	// tools key on this (e.g., busybox) — passing the resolved
	// path keeps them consistent with our Run* paths.
	argv := append([]string{full}, args...)
	env := os.Environ()
	if len(e.Env) > 0 {
		env = append(env, e.Env...)
	}
	return syscall.Exec(full, argv, env)
}

// logExec writes a single debug-level line per invocation when a
// logger is configured. SPEC §4.6 requires a stable `# exec: …`
// prefix so users grepping --debug output get one line per external
// process with the full argv. We emit the literal text rather than
// going through slog's TextHandler because the handler would prefix
// each line with `level=DEBUG msg=…` and key=value pairs that break
// the documented grep-by-`^# exec: ` shape.
//
// The line is gated on the logger having Debug level enabled, which
// in turn is controlled by the --debug global flag (NewLogger is
// constructed at Info by default). Without --debug the call is a
// cheap level check and nothing is written.
//
// We log the full path and the args joined by spaces. We don't log
// env — it can contain PGPASSWORD and other secrets, and the user
// can see what they passed via --debug at the command-construction
// layer.
func (e *Exec) logExec(full string, args []string) {
	if e.Logger == nil {
		return
	}
	if !e.Logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	if len(args) == 0 {
		fmt.Fprintf(debugExecWriter, "# exec: %s\n", full)
		return
	}
	fmt.Fprintf(debugExecWriter, "# exec: %s %s\n", full, strings.Join(args, " "))
}

// WithLogger attaches the given logger to the receiver and returns
// the receiver, enabling fluent construction:
//
//	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
//
// Nil is accepted and clears any previously attached logger. The
// chainable shape exists so existing call sites that don't care
// about exec logging keep working — the global-flags slice only
// adds .WithLogger(globals.Logger) where appropriate.
func (e *Exec) WithLogger(l *slog.Logger) *Exec {
	e.Logger = l
	return e
}

// exitCodeOf inspects the error from exec.Cmd.Run and decomposes
// it into (exitCode, residualErr). The residual err is nil when
// the child ran and returned an exit code cleanly (regardless of
// whether the code was zero); it is non-nil when something went
// wrong outside the child's control (lookup failure, fork failure,
// context cancellation).
func exitCodeOf(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Process ran but exited non-zero. The exit code is
		// available; this is a "clean" failure from our POV.
		return exitErr.ExitCode(), nil
	}
	// Could not even start the process, or the context was
	// cancelled mid-run. -1 distinguishes this from any real
	// child exit code (which the kernel ranges 0..255).
	return -1, err
}
