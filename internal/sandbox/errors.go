// Package-local mapping from internal errors to ui.ExitCode.
//
// The sandbox package is consumed by the cmd/pg_sandbox CLI layer,
// which is responsible for translating any error returned here into
// an os.Exit call. Rather than have each public function return a
// (DeployResult, ui.ExitCode, error) triple — easy to misuse, easy
// to drop the exit code on a refactor — we wrap the underlying error
// in an exitErr that carries the ui.ExitCode. The CLI layer pulls it
// back out with errors.As. Wrapping (vs. equality) preserves the
// caller-helpful context already attached via fmt.Errorf(%w).

package sandbox

import (
	"errors"
	"fmt"

	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// Re-export the ui.ExitCode constants this package returns under
// shorter names. These are values, not types — a type alias would
// also work, but constants keep call sites self-documenting (the
// returned value reads as ExitInitdbFailed, not ui.ExitInitdbFailed,
// in this package's own error messages).
const (
	ExitOK                 = ui.ExitOK
	ExitUsage              = ui.ExitUsage
	ExitBadConfig          = ui.ExitBadConfig
	ExitNotASandbox        = ui.ExitNotASandbox
	ExitSandboxExists      = ui.ExitSandboxExists
	ExitPortInUse          = ui.ExitPortInUse
	ExitNoFreePort         = ui.ExitNoFreePort
	ExitInitdbFailed       = ui.ExitInitdbFailed
	ExitPgctlFailed        = ui.ExitPgctlFailed
	ExitBasebackupFailed   = ui.ExitBasebackupFailed
	ExitSourceUnreachable  = ui.ExitSourceUnreachable
	ExitNotAStandby        = ui.ExitNotAStandby
	ExitPromoteFailed      = ui.ExitPromoteFailed
	ExitDestroyFailed      = ui.ExitDestroyFailed
	ExitNotATTY            = ui.ExitNotATTY
	ExitPsqlFailed         = ui.ExitPsqlFailed
	ExitPublicationFailed  = ui.ExitPublicationFailed
	ExitSubscriptionFailed = ui.ExitSubscriptionFailed
	ExitSchemaCopyFailed   = ui.ExitSchemaCopyFailed
)

// exitErr pairs a ui.ExitCode with an underlying error. The CLI
// layer uses errors.As to recover the code and map it onto os.Exit.
type exitErr struct {
	Code ui.ExitCode
	Err  error
}

func (e *exitErr) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("sandbox: exit %d", int(e.Code))
	}
	return e.Err.Error()
}

func (e *exitErr) Unwrap() error { return e.Err }

// wrapExit attaches a ui.ExitCode to err. If err is already an
// exitErr, the outer code wins (the deepest caller had the most
// context); we don't override.
func wrapExit(code ui.ExitCode, err error) error {
	var existing *exitErr
	if errors.As(err, &existing) {
		// Caller below us already chose a code; preserve it.
		return err
	}
	return &exitErr{Code: code, Err: err}
}

// ExitCodeFor extracts the ui.ExitCode embedded in err (via
// wrapExit). If no exitErr is present in the chain, ExitGeneric is
// returned — the CLI layer's safety net.
func ExitCodeFor(err error) ui.ExitCode {
	if err == nil {
		return ui.ExitOK
	}
	var ee *exitErr
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ui.ExitGeneric
}
