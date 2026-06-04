// Cluster destroy: tear down every member of a cluster in reverse
// order, then drop the manifest, then the cluster dir. SPEC §6.11
// `cluster destroy`.
//
// Sequencing per SPEC §6.11 + the implementation brief:
//
//  1. LoadCluster to discover members.
//  2. Iterate members in REVERSE order (member N down to member 0).
//  3. For each member: call sandbox.Destroy. Record successes.
//  4. If all succeeded: remove the manifest, then os.Remove the
//     cluster dir (now empty).
//  5. If any failed: leave the manifest in place. Return
//     ExitClusterDestroyPartial with a message naming the survivors.
//
// Reverse order matters for both topologies:
//
//   - Physical: standbys depend on the primary's slots. Destroying a
//     standby drops its slot on the primary (best-effort in
//     sandbox.Destroy). Dropping the primary first leaves stranded
//     standbys with no upstream and orphan slots that can't be
//     dropped.
//
//   - Logical: subscribers depend on the publisher being reachable
//     to clean up their remote subscription slots. Dropping the
//     publisher first means each subscriber's DROP SUBSCRIPTION has
//     to detach its slot before failing — which sandbox.Destroy
//     already does via ALTER SUBSCRIPTION ... SET (slot_name = NONE),
//     so logical CAN survive forward-order in degraded mode. We still
//     do reverse order because it's the cleanest path on both
//     topologies and the SPEC says so.

package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
)

// DestroyOptions captures the inputs to `cluster destroy`.
type DestroyOptions struct {
	// ClusterDir is the cluster to tear down.
	ClusterDir string
}

// Destroy implements `cluster destroy`. Returns nil on full tear-down,
// wrapExit(ExitClusterDestroyPartial) when one or more members
// survived, or wrapExit(ExitNotACluster) when the target dir isn't a
// cluster.
//
// Like sandbox.Destroy, confirmation lives in the CLI layer — Destroy
// here assumes the caller has already prompted (or --force was used).
func Destroy(ctx context.Context, runner pgexec.Runner, opts DestroyOptions, stderrW io.Writer) error {
	if opts.ClusterDir == "" {
		return wrapExit(ExitUsage,
			errors.New("cluster.Destroy: ClusterDir is required"))
	}
	m, err := loadClusterOrFail(opts.ClusterDir)
	if err != nil {
		return err
	}

	// Step 2: walk members in reverse order. The manifest's slice
	// order is "primary first, then standbys 1..N"; iterate from the
	// back so standbys/subscribers go before the primary/publisher.
	survivors := make([]string, 0, len(m.Members))
	for i := len(m.Members) - 1; i >= 0; i-- {
		member := m.Members[i]
		dir := filepath.Join(opts.ClusterDir, member.Name)

		// Skip members whose dir has already been removed manually —
		// the user may have torn one down by hand. We log it so the
		// state isn't silent.
		if !config.IsSandboxDir(dir) {
			fmt.Fprintf(stderrW,
				"level=INFO msg=%q member=%q dir=%q\n",
				"cluster destroy: member dir missing or not a sandbox; skipping",
				member.Name, dir)
			continue
		}

		fmt.Fprintf(stderrW, "level=INFO msg=%q member=%q index=%d\n",
			"cluster destroy: tearing down member", member.Name, i)

		// Step 3: per-member destroy. We use the caller's Runner so
		// test Fakes see every call in one place. sandbox.Destroy is
		// best-effort for slot/subscription cleanup at sources, so
		// even though we destroy in reverse the source is still
		// alive when standby/subscriber cleanup runs.
		if dErr := sandbox.Destroy(ctx, runner, sandbox.DestroyOptions{SandboxDir: dir}, stderrW); dErr != nil {
			fmt.Fprintf(stderrW,
				"level=ERROR msg=%q member=%q err=%q\n",
				"cluster destroy: member destroy failed; will report as partial",
				member.Name, dErr.Error())
			survivors = append(survivors, member.Name)
		}
	}

	if len(survivors) > 0 {
		// Step 5: leave manifest in place so the user can finish by
		// hand or rerun once the underlying issue is fixed.
		return wrapExit(ExitClusterDestroyPartial,
			fmt.Errorf("cluster destroy partial: surviving members: %s",
				strings.Join(survivors, ", ")))
	}

	// Step 4: every member destroyed cleanly. Drop the manifest then
	// the cluster dir. The dir SHOULD be empty by now — every member
	// subdir was rm -rf'd by sandbox.Destroy, and the only other
	// entry is the manifest we just dropped.
	manifestPath := filepath.Join(opts.ClusterDir, config.ClusterFilename)
	if err := os.Remove(manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return wrapExit(ExitClusterDestroyPartial,
			fmt.Errorf("cluster destroy: remove manifest %s: %w", manifestPath, err))
	}
	if err := os.Remove(opts.ClusterDir); err != nil {
		// If the dir isn't empty (stray files the user dropped in),
		// we return partial — those files are diagnostic and we don't
		// rm -rf the cluster dir without warning. The manifest is
		// already gone, but the user can see survivors here.
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderrW,
				"level=WARN msg=%q dir=%q err=%q\n",
				"cluster destroy: members removed but cluster dir not empty",
				opts.ClusterDir, err.Error())
			return wrapExit(ExitClusterDestroyPartial,
				fmt.Errorf("cluster destroy: cluster dir not empty (manifest removed): %w", err))
		}
	}
	fmt.Fprintf(stderrW, "level=INFO msg=%q name=%q dir=%q\n",
		"cluster destroyed", m.Name, opts.ClusterDir)
	return nil
}
