// Cluster deploy: provision N+1 sandboxes wired together as a unit.
// SPEC §6.11 `cluster deploy`.
//
// Two topologies are supported:
//
//   - Physical: member 0 is a standalone primary; members 1..N are
//     physical standbys streaming from member 0 via pg_basebackup +
//     replication slots. Slot names follow physicalSlotName (see
//     cluster.go).
//
//   - Logical: member 0 is a publisher; members 1..N are subscribers
//     attached to a single publication on member 0. The publication
//     is created on member 0 BEFORE any subscriber is deployed so the
//     subscribers' initial copy can find the publication on the wire.
//
// Sequencing per SPEC §6.11:
//
//  1. Validate inputs; refuse if cluster dir exists.
//  2. Create cluster dir.
//  3. Deploy member 0 (primary or publisher).
//  4. If logical: run `publish --all-tables` on member 0.
//  5. For i in 1..N: deploy member i. On failure, write the manifest
//     with members deployed so far and return ExitClusterDeployFailed.
//  6. On full success: write the final manifest.
//
// Failure handling: SPEC §6.11 + the implementation brief specify
// "leave partial state on disk for inspection, return
// ExitClusterDeployFailed". We do NOT auto-rollback because that would
// risk destroying the diagnostic evidence the user needs.
//
// --sync-count > 0 is parsed and we emit a single warn-level line
// telling the user synchronous-standby support is deferred to a
// follow-up slice; we proceed with all members as async. The brief
// pins this explicitly.

package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
)

// DeployOptions captures every input that influences `cluster deploy`.
// The CLI layer populates this from flag parsing; the cluster package
// never reads flag.FlagSet directly. Mirrors the shape of
// sandbox.DeployOptions for the per-member fields plus cluster-level
// knobs.
type DeployOptions struct {
	// ClusterDir is the absolute path where the new cluster lives.
	// Created by Deploy; must not already exist (or must be empty).
	ClusterDir string

	// BinDir is the PostgreSQL bin/ directory used for every member.
	BinDir string

	// Nodes is the count of standbys/subscribers (members 1..N).
	// Member 0 (primary/publisher) is implicit. Must be >= 1.
	Nodes int

	// Host applies to member 0; subsequent members inherit it.
	Host string

	// Port applies to member 0 only; subsequent members auto-allocate
	// via portalloc starting one above the previous member's port.
	Port int

	// PortExplicit is true iff the user supplied --port on the CLI.
	// Same semantics as sandbox.DeployOptions.PortExplicit.
	PortExplicit bool

	// Superuser, Dbname apply to member 0 and are inherited by all
	// other members so a `use` against any member connects with the
	// same credentials.
	Superuser string
	Dbname    string

	// Mode is the cluster topology (physical or logical).
	Mode config.ClusterMode

	// SlotPrefix is the prefix used for physical replication slot
	// names. Empty defaults to the cluster name (SPEC §6.11). Only
	// meaningful when Mode == ClusterPhysical.
	SlotPrefix string

	// PubName is the publication name created on member 0 and
	// attached to by all subscribers. Only meaningful when Mode ==
	// ClusterLogical. Empty defaults to "pgs_pub".
	PubName string

	// SyncCount is the number of first standbys that should be
	// synchronous. This slice accepts the value, warns when > 0, and
	// proceeds async — see file-level doc comment.
	SyncCount int

	// SelfPath is the absolute path of the pg_sandbox binary that's
	// performing this deploy. Propagated to each sandbox.Deploy call
	// so the convenience scripts inside member dirs invoke the same
	// binary. See sandbox.DeployOptions.SelfPath.
	SelfPath string
}

// Deploy is `cluster deploy`'s entry point. See file-level doc
// comment for sequencing and failure handling.
//
// runner is used for every per-member sandbox.Deploy and (in logical
// mode) for the publish step against member 0. Passing one Runner
// makes test Fakes able to intercept every external call in one
// place.
func Deploy(ctx context.Context, runner pgexec.Runner, opts DeployOptions, stderrW io.Writer) (*config.ClusterManifest, error) {
	if err := normalizeDeployOptions(&opts); err != nil {
		return nil, err
	}

	// SPEC §6.11 step 1: refuse if the cluster dir already exists and
	// is non-empty. We mirror sandbox.Deploy's "empty dir is okay"
	// semantics so users can pre-create with specific permissions.
	if err := checkClusterDirAvailable(opts.ClusterDir); err != nil {
		return nil, err
	}

	if opts.SyncCount > 0 {
		// Brief pins this: accept the flag, warn, proceed async.
		fmt.Fprintf(stderrW,
			"level=WARN msg=%q sync_count=%d\n",
			"synchronous-standby support deferred to a follow-up slice; treating as async",
			opts.SyncCount)
	}

	// SPEC §6.11 step 2: create the cluster dir.
	if err := os.MkdirAll(opts.ClusterDir, 0o755); err != nil {
		return nil, fmt.Errorf("cluster.Deploy: mkdir %s: %w", opts.ClusterDir, err)
	}

	cluster := clusterName(opts.ClusterDir)
	members := make([]config.ClusterMember, 0, opts.Nodes+1)

	// SPEC §6.11 step 3: deploy member 0 (primary or publisher).
	primaryName := memberName(cluster, 0)
	primaryDir := memberDir(opts.ClusterDir, 0)
	fmt.Fprintf(stderrW, "level=INFO msg=%q member=%q index=0\n",
		"cluster: deploying primary/publisher", primaryName)

	primaryOpts := sandbox.DeployOptions{
		SandboxDir:   primaryDir,
		BinDir:       opts.BinDir,
		Host:         opts.Host,
		Port:         opts.Port,
		PortExplicit: opts.PortExplicit,
		Superuser:    opts.Superuser,
		Dbname:       opts.Dbname,
		SelfPath:     opts.SelfPath,
		ClusterName:  cluster,
	}
	primaryRes, err := sandbox.Deploy(ctx, runner, primaryOpts, stderrW)
	if err != nil {
		// Member 0 failed; no manifest to write (no members deployed).
		// The half-created cluster dir + any partial member 0 stub
		// stays on disk for inspection.
		return nil, wrapExit(ExitClusterDeployFailed,
			fmt.Errorf("cluster: deploy member 0 (%s): %w", primaryName, err))
	}
	primaryRole := config.RolePrimary
	if opts.Mode == config.ClusterLogical {
		primaryRole = config.RolePublisher
	}
	members = append(members, config.ClusterMember{
		Name: primaryName,
		Role: primaryRole,
	})

	// SPEC §6.11 step 4 (logical mode): publish on member 0 BEFORE
	// any subscriber is deployed. Subscribers' CREATE SUBSCRIPTION
	// needs the publication to exist on the wire; if we published
	// after deploying subscribers, their initial copy_data would
	// race the publication into existence.
	if opts.Mode == config.ClusterLogical {
		fmt.Fprintf(stderrW, "level=INFO msg=%q pub=%q publisher=%q\n",
			"cluster: creating publication on member 0", opts.PubName, primaryName)
		pubErr := sandbox.Publish(ctx, runner, sandbox.PublishOptions{
			SandboxDir: primaryDir,
			PubName:    opts.PubName,
			AllTables:  true,
		}, stderrW)
		if pubErr != nil {
			// Publish failure: persist what we have (just member 0) and
			// bail with the cluster-level exit code. The publisher
			// sandbox stays for inspection.
			_ = saveManifest(opts.ClusterDir, cluster, opts.Mode, members, opts, stderrW)
			return nil, wrapExit(ExitClusterDeployFailed,
				fmt.Errorf("cluster: publish on member 0: %w", pubErr))
		}
	}

	// SPEC §6.11 step 5: deploy members 1..N.
	//
	// Per-member port: PortExplicit is false here so each member
	// auto-allocates. We start each member's scan one port above the
	// previous member's resolved port so a tight range of busy ports
	// near the base doesn't force every member to walk the whole
	// scan range. portalloc.IsBusy is the actual conflict check, so
	// even with overlapping scans no two members can land on the same
	// port.
	prevPort := primaryRes.Sandbox.Port
	for i := 1; i <= opts.Nodes; i++ {
		name := memberName(cluster, i)
		dir := memberDir(opts.ClusterDir, i)
		fmt.Fprintf(stderrW, "level=INFO msg=%q member=%q index=%d\n",
			"cluster: deploying member", name, i)

		memberOpts := sandbox.DeployOptions{
			SandboxDir:   dir,
			BinDir:       opts.BinDir,
			Host:         opts.Host,
			PortBase:     prevPort + 1,
			Superuser:    opts.Superuser,
			Dbname:       opts.Dbname,
			SelfPath:     opts.SelfPath,
			ClusterName:  cluster,
			PortExplicit: false,
		}
		switch opts.Mode {
		case config.ClusterPhysical:
			memberOpts.ReplicateFrom = primaryName
			memberOpts.SlotName = physicalSlotName(opts.SlotPrefix, name)
		case config.ClusterLogical:
			memberOpts.SubscribeTo = primaryName
			memberOpts.PubName = opts.PubName
			// SubName defaults to "<member-name>_sub" inside
			// sandbox.Subscribe; we don't override it here so the
			// default is what hits pg_subscription.
		}

		memberRes, mErr := sandbox.Deploy(ctx, runner, memberOpts, stderrW)
		if mErr != nil {
			// Partial deploy: persist a manifest reflecting the
			// members that DID make it so the on-disk state and the
			// manifest agree. Then return the cluster-level error.
			fmt.Fprintf(stderrW, "level=ERROR msg=%q member=%q index=%d err=%q\n",
				"cluster: member deploy failed; leaving partial cluster for inspection",
				name, i, mErr.Error())
			if writeErr := saveManifest(opts.ClusterDir, cluster, opts.Mode, members, opts, stderrW); writeErr != nil {
				fmt.Fprintf(stderrW, "level=WARN msg=%q err=%q\n",
					"cluster: could not write partial manifest", writeErr.Error())
			}
			return nil, wrapExit(ExitClusterDeployFailed,
				fmt.Errorf("cluster: deploy member %d (%s): %w", i, name, mErr))
		}

		memberRole := config.RoleStandby
		if opts.Mode == config.ClusterLogical {
			memberRole = config.RoleSubscriber
		}
		members = append(members, config.ClusterMember{
			Name: name,
			Role: memberRole,
		})
		prevPort = memberRes.Sandbox.Port
	}

	// SPEC §6.11 step 6: write the final manifest.
	manifest, err := buildManifest(cluster, opts.Mode, members, opts)
	if err != nil {
		return nil, fmt.Errorf("cluster.Deploy: build manifest: %w", err)
	}
	if err := config.SaveCluster(opts.ClusterDir, manifest); err != nil {
		return nil, fmt.Errorf("cluster.Deploy: save manifest: %w", err)
	}

	fmt.Fprintf(stderrW, "level=INFO msg=%q name=%q mode=%q members=%d\n",
		"cluster deployed", cluster, opts.Mode, len(members))
	return manifest, nil
}

// normalizeDeployOptions fills in defaults for any zero-valued field
// and rejects misuse the caller can't recover from. Mirrors
// sandbox.normalizeDeployOptions in spirit.
func normalizeDeployOptions(opts *DeployOptions) error {
	if opts.ClusterDir == "" {
		return wrapExit(ExitUsage, errors.New("cluster.Deploy: ClusterDir is required"))
	}
	if opts.BinDir == "" {
		return wrapExit(ExitUsage, errors.New("cluster.Deploy: BinDir is required"))
	}
	if opts.Nodes < 1 {
		return wrapExit(ExitUsage,
			fmt.Errorf("cluster.Deploy: --nodes must be >= 1, got %d", opts.Nodes))
	}
	if opts.Mode == "" {
		opts.Mode = config.ClusterPhysical
	}
	switch opts.Mode {
	case config.ClusterPhysical, config.ClusterLogical:
		// ok
	default:
		return wrapExit(ExitUsage,
			fmt.Errorf("cluster.Deploy: unknown mode %q", opts.Mode))
	}
	if opts.SyncCount < 0 {
		return wrapExit(ExitUsage,
			fmt.Errorf("cluster.Deploy: --sync-count must be >= 0, got %d", opts.SyncCount))
	}
	// Default SlotPrefix to the cluster name per SPEC §6.11.
	if opts.SlotPrefix == "" {
		opts.SlotPrefix = clusterName(opts.ClusterDir)
	}
	// Default PubName to "pgs_pub" per SPEC §6.11.
	if opts.PubName == "" {
		opts.PubName = "pgs_pub"
	}
	return nil
}

// checkClusterDirAvailable enforces SPEC §6.11's "refuse if cluster
// dir exists" failure mode. Like sandbox.checkSandboxDirAvailable, an
// existing empty directory is acceptable so callers who pre-create
// with specific permissions aren't blocked.
func checkClusterDirAvailable(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cluster.Deploy: stat %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return wrapExit(ExitClusterExists,
			fmt.Errorf("cluster dir %s is not empty", dir))
	}
	return nil
}

// buildManifest assembles a fully-populated ClusterManifest from
// member list + options. Pulled out so Deploy and the partial-save
// failure path share one source of truth for the manifest shape.
func buildManifest(cluster string, mode config.ClusterMode, members []config.ClusterMember, opts DeployOptions) (*config.ClusterManifest, error) {
	repl := config.ClusterRepl{
		SyncCount: opts.SyncCount,
	}
	switch mode {
	case config.ClusterPhysical:
		repl.SlotPrefix = opts.SlotPrefix
	case config.ClusterLogical:
		repl.PublicationName = opts.PubName
	}
	return &config.ClusterManifest{
		SchemaVersion: config.CurrentSchemaVersion,
		Name:          cluster,
		Mode:          mode,
		Members:       members,
		Replication:   repl,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// saveManifest is a convenience wrapper around buildManifest +
// config.SaveCluster used by the partial-failure path so the on-disk
// manifest matches the members actually deployed.
func saveManifest(clusterDir, cluster string, mode config.ClusterMode, members []config.ClusterMember, opts DeployOptions, stderrW io.Writer) error {
	m, err := buildManifest(cluster, mode, members, opts)
	if err != nil {
		return err
	}
	if err := config.SaveCluster(clusterDir, m); err != nil {
		return err
	}
	fmt.Fprintf(stderrW, "level=INFO msg=%q name=%q members=%d\n",
		"cluster: partial manifest written", cluster, len(members))
	return nil
}
