// Package-local mapping from internal errors to ui.ExitCode.
//
// Same pattern as internal/sandbox/errors.go and
// internal/cluster/cluster.go: wrap the underlying error in an
// exitErr that carries the ui.ExitCode, then the CLI layer pulls it
// back out with errors.As + ExitCodeFor.

package report

import (
	"errors"
	"fmt"

	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// exitErr pairs a ui.ExitCode with an underlying error.
type exitErr struct {
	Code ui.ExitCode
	Err  error
}

func (e *exitErr) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("report: exit %d", int(e.Code))
	}
	return e.Err.Error()
}

func (e *exitErr) Unwrap() error { return e.Err }

// ExitCodeFor walks the error chain and returns the embedded
// ui.ExitCode. If the chain originated from sandbox.Deploy /
// sandbox.Destroy (which wrap their own exit codes), we delegate to
// sandbox.ExitCodeFor so the CLI sees the right code for that sub-
// pipeline failure. ExitGeneric is the safety net.
func ExitCodeFor(err error) ui.ExitCode {
	if err == nil {
		return ui.ExitOK
	}
	var ee *exitErr
	if errors.As(err, &ee) {
		return ee.Code
	}
	// Delegate to sandbox.ExitCodeFor so a Deploy/Destroy failure
	// surfaces its own code (e.g. ExitInitdbFailed) rather than a
	// generic ExitReportFailed. This makes diagnostic mapping
	// transparent across the pipeline.
	if code := sandbox.ExitCodeFor(err); code != ui.ExitGeneric {
		return code
	}
	return ui.ExitGeneric
}
