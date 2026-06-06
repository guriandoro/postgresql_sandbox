// Fake Runner for tests.
//
// A Fake records every call (binary name, argv, captured stdin) and
// returns canned Results so command tests can assert "deploy
// invoked initdb with these flags" without launching a real
// PostgreSQL. This is the second tier of the testing strategy laid
// out in SPEC §9 — unit tests use Fake for hermetic, fast feedback.
//
// Design notes:
//
//   - The Fake satisfies Runner. Drop it into any code that takes a
//     Runner. There is no factory; zero-value works.
//
//   - Canned results are looked up by binary name (not by full
//     argv). Test setup is typically `f.SetResult("psql", Result{Stdout: ...})`.
//     If a test needs argv-conditional responses, it can iterate
//     f.Calls after the fact and assert on the captured argv.
//
//   - Exec (the syscall.Exec replacement) on a Fake records the
//     call and returns nil — the test process is not actually
//     replaced. Callers that depend on Exec never returning would
//     need a different fake strategy; we treat that path as
//     untested-here and verified separately in integration tests.

package pgexec

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// FakeCall is one captured invocation.
type FakeCall struct {
	// Method records which Runner method was called. Useful when a
	// test wants to assert "deploy used RunInteractive for X but
	// Run for Y".
	Method string

	// Name is the binary name (or path) as the caller passed it,
	// NOT the resolved absolute path — Fake doesn't run Locate.
	// This makes argv assertions readable.
	Name string

	// Args is the argv after the binary name. Comparable directly
	// to []string{...} literals in tests.
	Args []string

	// Stdin captures whatever the caller passed via RunWithStdin,
	// fully drained. nil for the other methods.
	Stdin []byte
}

// Fake implements Runner without launching any process. Zero value
// is ready to use.
type Fake struct {
	// Calls is appended-to on every invocation. Tests assert on
	// len(f.Calls) and individual entries.
	Calls []FakeCall

	// Results maps binary name → Result that will be returned on
	// the next call to Run* for that name. Unset binaries return
	// the zero Result (exit 0, no output).
	Results map[string]Result

	// LocateErr, if non-nil, is returned from Locate for any
	// binary name. Use it to simulate "psql not found in BinDir
	// or PATH".
	LocateErr error
}

// SetResult is a convenience for tests that want to register one
// canned Result without juggling a map literal.
func (f *Fake) SetResult(name string, r Result) {
	if f.Results == nil {
		f.Results = map[string]Result{}
	}
	f.Results[name] = r
}

// Locate satisfies Runner. It either returns f.LocateErr (so tests
// can assert "deploy failed because initdb was missing") or pretends
// the binary is at "/fake/<name>".
func (f *Fake) Locate(name string) (string, error) {
	if f.LocateErr != nil {
		return "", f.LocateErr
	}
	return "/fake/" + name, nil
}

// Run satisfies Runner.Run.
func (f *Fake) Run(_ context.Context, name string, args ...string) Result {
	f.Calls = append(f.Calls, FakeCall{Method: "Run", Name: name, Args: args})
	return f.lookup(name)
}

// RunWithStdin satisfies Runner.RunWithStdin. It eagerly drains
// stdin so tests can assert on what was piped (without this,
// callers that close stdin late would race with the test).
func (f *Fake) RunWithStdin(_ context.Context, stdin io.Reader, name string, args ...string) Result {
	var drained []byte
	if stdin != nil {
		var err error
		drained, err = io.ReadAll(stdin)
		if err != nil {
			// Surface the read error as a Result.Err — tests can
			// notice it. Real callers never produce broken stdin
			// readers in practice, but it's better than panicking.
			return Result{ExitCode: -1, Err: fmt.Errorf("fake: stdin read: %w", err)}
		}
	}
	f.Calls = append(f.Calls, FakeCall{
		Method: "RunWithStdin",
		Name:   name,
		Args:   args,
		Stdin:  drained,
	})
	return f.lookup(name)
}

// RunInteractive satisfies Runner.RunInteractive.
func (f *Fake) RunInteractive(_ context.Context, name string, args ...string) Result {
	f.Calls = append(f.Calls, FakeCall{Method: "RunInteractive", Name: name, Args: args})
	return f.lookup(name)
}

// Exec satisfies Runner.Exec. We record the call and return nil so
// the test continues. A test that wants to assert "we tried to
// exec" inspects f.Calls; a test that wants to assert "we used
// exec semantics (no return)" needs an integration test instead.
//
// We deliberately do NOT propagate the lookup error here even when
// LocateErr is set — Exec callers' contract with the real Runner
// is "non-nil return means it failed before exec'ing", so we
// preserve that.
func (f *Fake) Exec(name string, args ...string) error {
	f.Calls = append(f.Calls, FakeCall{Method: "Exec", Name: name, Args: args})
	if f.LocateErr != nil {
		return f.LocateErr
	}
	return nil
}

// lookup returns the canned Result for name, or zero if none set.
func (f *Fake) lookup(name string) Result {
	if f.Results == nil {
		return Result{}
	}
	if r, ok := f.Results[name]; ok {
		return r
	}
	return Result{}
}

// ErrFakeNoSuchBinary is a convenience error for tests that want to
// preload a "psql is missing" scenario.
var ErrFakeNoSuchBinary = errors.New("fake: binary not present")
