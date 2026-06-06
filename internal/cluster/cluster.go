// Shared types and helpers for cluster deploy / status / destroy.
//
// Each operation lives in its own file (deploy.go, status.go,
// destroy.go) for the same reason the sandbox package splits its
// commands: a reviewer can pull one file open and read the entire
// flow without skipping past unrelated code. This file holds the
// types they share — the Options structs are deliberately split per
// command so callers cannot accidentally pass deploy flags into a
// destroy and have them silently ignored.

package cluster

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// Re-export the ui.ExitCode constants this package returns under
// shorter names. Same pattern as internal/sandbox/errors.go — keeps
// returned errors readable in error messages without sprinkling the
// ui. prefix everywhere.
const (
	ExitOK                    = ui.ExitOK
	ExitUsage                 = ui.ExitUsage
	ExitNotACluster           = ui.ExitNotACluster
	ExitClusterExists         = ui.ExitClusterExists
	ExitClusterDeployFailed   = ui.ExitClusterDeployFailed
	ExitClusterDestroyPartial = ui.ExitClusterDestroyPartial
	ExitBadConfig             = ui.ExitBadConfig
	ExitInitSQLFailed         = ui.ExitInitSQLFailed
)

// exitErr pairs a ui.ExitCode with an underlying error so the CLI
// layer can recover the right exit code via errors.As. Same shape as
// sandbox.exitErr — duplicated here rather than imported because the
// type is package-local at sandbox and exporting it would broaden a
// surface area that doesn't need to be shared.
type exitErr struct {
	Code ui.ExitCode
	Err  error
}

func (e *exitErr) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("cluster: exit %d", int(e.Code))
	}
	return e.Err.Error()
}

func (e *exitErr) Unwrap() error { return e.Err }

// wrapExit attaches code to err unless err already carries an
// exitErr (in which case the inner code wins — caller had the most
// specific context).
func wrapExit(code ui.ExitCode, err error) error {
	var existing *exitErr
	if errors.As(err, &existing) {
		return err
	}
	return &exitErr{Code: code, Err: err}
}

// ExitCodeFor extracts the ui.ExitCode embedded in err (via
// wrapExit). If no exitErr is present in the chain, ExitGeneric is
// returned — the CLI layer's safety net. Mirrors
// sandbox.ExitCodeFor.
func ExitCodeFor(err error) ui.ExitCode {
	if err == nil {
		return ui.ExitOK
	}
	var ee *exitErr
	if errors.As(err, &ee) {
		return ee.Code
	}
	// If a sandbox.exitErr made it up here (cluster Deploy delegates
	// to sandbox.Deploy), the caller wants that code. ExitCodeFor is
	// supposed to walk the chain; errors.As does that for us.
	return ui.ExitGeneric
}

// clusterName returns the canonical cluster name for the given
// cluster dir. We always use filepath.Base — SPEC §6.11 says the
// cluster name is the basename of the cluster dir; codifying it here
// keeps every caller in sync.
func clusterName(clusterDir string) string {
	return filepath.Base(clusterDir)
}

// memberName builds the on-disk name for a member, given the cluster
// name and the member's role within the topology:
//
//   - index 0 → "<cluster>_p" (primary / publisher)
//   - index i (≥1) → "<cluster>_s<i>" (standby / subscriber)
//
// The "_p" / "_s<n>" suffix convention is fixed by SPEC §4.4's
// "<member-N>" placeholder plus the brief's filesystem-layout
// example. Centralized here so future callers don't drift.
func memberName(cluster string, index int) string {
	if index == 0 {
		return cluster + "_p"
	}
	return fmt.Sprintf("%s_s%d", cluster, index)
}

// memberDir returns the absolute path of member i under clusterDir.
func memberDir(clusterDir string, i int) string {
	return filepath.Join(clusterDir, memberName(clusterName(clusterDir), i))
}

// physicalSlotName builds the canonical replication slot name for a
// physical standby member. Format: <slot-prefix>_<member-name>_slot
// (e.g. cluster "mycluster", member "mycluster_s1" → slot
// "mycluster_mycluster_s1_slot"). The double cluster-name segment
// looks redundant but mirrors the per-sandbox slot convention the
// physical-replication slice settled on (see commit 37a5fe4) — a slot
// name that includes both cluster prefix and member name reads
// unambiguously in pg_replication_slots on the primary.
func physicalSlotName(slotPrefix, member string) string {
	return fmt.Sprintf("%s_%s_slot", slotPrefix, member)
}

// loadClusterOrFail returns the cluster manifest at clusterDir or a
// wrapped ExitNotACluster error if no manifest is present. Mirrors
// sandbox.loadSandboxOrFail.
func loadClusterOrFail(clusterDir string) (*config.ClusterManifest, error) {
	if !config.IsClusterDir(clusterDir) {
		return nil, wrapExit(ExitNotACluster,
			fmt.Errorf("not a cluster: %s", clusterDir))
	}
	m, err := config.LoadCluster(clusterDir)
	if err != nil {
		return nil, wrapExit(ExitBadConfig,
			fmt.Errorf("cluster: load manifest: %w", err))
	}
	return m, nil
}
