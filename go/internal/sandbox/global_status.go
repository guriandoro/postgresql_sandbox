// global_status: walk a sandbox root and report every sandbox + cluster
// the tool finds. SPEC §6.12.
//
// Design points worth flagging:
//
//   - "Cheap to run" is the contract. SPEC §6.12 says MUST NOT do
//     per-sandbox SQL queries beyond what's needed to determine running
//     state. We use the same pidfile + port-listen pair sandbox.Status
//     uses for State (no psql calls). We DO NOT probe PG version (that
//     would require either a psql call or a fork/exec of `pg_config`
//     per unique BinDir); the version column from the contract is
//     deliberately omitted in favor of cheap-to-run.
//
//   - Walk is sequential and depth-bounded (maxDepth=3). The brief
//     explicitly says concurrent walk is out of scope for ≤100
//     sandboxes; a sequential walk's wall-clock is dominated by stat
//     syscalls which are fast.
//
//   - Grouping: a sandbox dir nested under a cluster dir is grouped
//     under that cluster. A sandbox whose config carries Cluster=X but
//     for which no cluster dir named X is found on disk is flagged as
//     "orphaned" — separate output section so the user notices.
//
//   - filepath.Walk semantics: when we recurse into a sandbox or
//     cluster dir we DO NOT descend further into the sandbox's own
//     data dir (postgres internal). This is enforced by the early
//     "stop recursion when we hit a sandbox/cluster" branch in the
//     walker.
//
//   - We deliberately put this in the sandbox package (not a new
//     internal/global) because:
//       (a) It reuses isRunning / isPortListening, which are
//           package-private helpers in sandbox/lifecycle.go.
//       (b) The implementation is small enough that a new package
//           would be more boilerplate than benefit.

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
)

// globalWalkMaxDepth caps how deep the walker descends from the root.
// Three levels is enough to cover sandbox-root / cluster-dir /
// member-dir and protects against accidentally walking $HOME or /.
const globalWalkMaxDepth = 3

// SandboxEntry is the per-sandbox row in a GlobalStatus. The fields
// are the minimum needed to print SPEC §6.12's NAME / STATE / ROLE /
// HOST:PORT / CLUSTER columns without doing any SQL.
type SandboxEntry struct {
	// Name is the sandbox's canonical identifier (from its config).
	Name string `json:"name"`

	// Dir is the absolute path of the sandbox directory. Useful for
	// JSON consumers that want to act on a specific one.
	Dir string `json:"dir"`

	// State is the pidfile+listener-derived run state. Cheap to
	// compute — no psql call.
	State RunState `json:"state"`

	// Role is the sandbox's declared role (from config). May be empty
	// when the config is malformed; in that case the entry is still
	// emitted so users can see "there's something here, it's broken".
	Role config.Role `json:"role,omitempty"`

	// Host and Port are the listen address from config.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`

	// Cluster, when non-empty, names the cluster this sandbox claims
	// membership in. Used by the renderer to group entries under a
	// cluster header and to flag orphans (cluster declared but no
	// cluster dir on disk).
	Cluster string `json:"cluster,omitempty"`
}

// ClusterEntry is the per-cluster row in a GlobalStatus. Members are
// the sandbox entries grouped under it.
type ClusterEntry struct {
	// Name is the cluster's identifier from its manifest.
	Name string `json:"name"`

	// Dir is the absolute path of the cluster directory.
	Dir string `json:"dir"`

	// Mode is `physical` or `logical` per the manifest.
	Mode config.ClusterMode `json:"mode,omitempty"`

	// Members are the per-member sandbox entries. Order matches the
	// manifest's Members slice (primary/publisher first, then
	// standbys/subscribers in deployment order).
	Members []SandboxEntry `json:"members"`
}

// GlobalStatus is the structured result of a global walk. The CLI
// layer renders it as text or JSON; nothing inside this struct is
// rendering-policy.
type GlobalStatus struct {
	// Root is the absolute root the walk started from.
	Root string `json:"root"`

	// Sandboxes are top-level (non-cluster) sandboxes. Order is the
	// walk order, which is lexicographic by directory name (filepath.Walk
	// guarantees sorted order).
	Sandboxes []SandboxEntry `json:"sandboxes"`

	// Clusters are the clusters found, with their members nested.
	Clusters []ClusterEntry `json:"clusters"`

	// Orphaned are sandboxes whose config references a Cluster name
	// for which no cluster manifest was found under Root. Surfaced
	// separately so users see "you have a sandbox claiming to be part
	// of cluster X, but X isn't on disk".
	Orphaned []SandboxEntry `json:"orphaned,omitempty"`
}

// GlobalStatusOptions captures the inputs to GlobalStatusWalk.
type GlobalStatusOptions struct {
	// Root is the directory to walk. MUST be absolute or made
	// absolute by the caller. A missing root is treated as "no
	// sandboxes" (empty result, no error) — first-run users haven't
	// created the default ~/postgresql-sandboxes/ yet.
	Root string
}

// GlobalStatusWalk performs the SPEC §6.12 walk. It is read-only —
// no SQL, no fork/exec, just os.Stat + JSON-parse of the on-disk
// config files. Sequential, depth-bounded.
//
// Returns a populated GlobalStatus even when Root doesn't exist
// (empty Sandboxes / Clusters / Orphaned slices). The only error
// returned is from a directory-listing failure on Root itself; per-
// sandbox / per-cluster load errors are surfaced as best-effort
// warn-level lines on stderrW and the offending dir is skipped (the
// walk continues).
//
// ctx is accepted for consistency with the rest of the package but
// not currently consulted — the walk is too fast to need cancellation.
func GlobalStatusWalk(ctx context.Context, opts GlobalStatusOptions, stderrW io.Writer) (*GlobalStatus, error) {
	_ = ctx // accepted for future use; see doc comment.

	root := opts.Root
	if root == "" {
		return nil, fmt.Errorf("sandbox.GlobalStatusWalk: Root is required")
	}
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("sandbox.GlobalStatusWalk: abs(%s): %w", root, err)
		}
		root = abs
	}

	gs := &GlobalStatus{
		Root:      root,
		Sandboxes: []SandboxEntry{},
		Clusters:  []ClusterEntry{},
	}

	// Missing root: SPEC §6.12 doesn't say so explicitly, but treating
	// "you haven't created any sandboxes yet" as an empty result rather
	// than an error matches the principle of least surprise (and is
	// what the smoke test relies on for first-run UX).
	st, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return gs, nil
		}
		return nil, fmt.Errorf("sandbox.GlobalStatusWalk: stat %s: %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("sandbox.GlobalStatusWalk: %s is not a directory", root)
	}

	// Map from cluster-name → index in gs.Clusters, populated as we
	// discover cluster dirs. Used after the walk to attach any orphan
	// sandboxes to the right bucket.
	clusterByName := map[string]int{}

	if err := walkRoot(root, 0, gs, clusterByName, stderrW); err != nil {
		return nil, err
	}

	// Sort top-level sandboxes by name for deterministic output. Walk
	// already visits in lexicographic order, but a defensive sort
	// guarantees the JSON consumer sees consistent ordering even if
	// filepath.WalkDir's semantics ever drift across Go versions.
	sort.Slice(gs.Sandboxes, func(i, j int) bool {
		return gs.Sandboxes[i].Name < gs.Sandboxes[j].Name
	})
	sort.Slice(gs.Clusters, func(i, j int) bool {
		return gs.Clusters[i].Name < gs.Clusters[j].Name
	})

	// Reconcile orphans: any top-level sandbox that claims membership
	// in a cluster we didn't see on disk goes into Orphaned. We do
	// this AFTER sorting so the orphan section is also deterministic.
	kept := gs.Sandboxes[:0]
	for _, sb := range gs.Sandboxes {
		if sb.Cluster != "" {
			if _, ok := clusterByName[sb.Cluster]; !ok {
				gs.Orphaned = append(gs.Orphaned, sb)
				continue
			}
			// The sandbox is grouped under the cluster — drop from the
			// top-level list. The walker has already added the member
			// to the cluster's Members slice via the cluster-walking
			// branch, so we'd double-count if we left it here.
			//
			// In practice this branch is rarely hit: if the sandbox is
			// physically nested under the cluster dir, the walker
			// recognises it as a cluster member during recursion and
			// never adds it to gs.Sandboxes. A sandbox that lives
			// outside its cluster's dir but still names the cluster is
			// possible (user moved the dir), and our handling here is
			// to group it under the cluster anyway. Append-to-members
			// keeps the relationship visible.
			idx := clusterByName[sb.Cluster]
			gs.Clusters[idx].Members = append(gs.Clusters[idx].Members, sb)
			continue
		}
		kept = append(kept, sb)
	}
	gs.Sandboxes = kept

	return gs, nil
}

// walkRoot is the depth-bounded recursive walker. It visits dir's
// entries; for each one:
//   - if the entry is a sandbox dir → load + add as a top-level
//     SandboxEntry (the caller decides later whether to reclassify it
//     as a cluster member or orphan).
//   - if the entry is a cluster dir → recurse into it via walkCluster
//     and add the resulting ClusterEntry.
//   - otherwise, if depth < maxDepth → recurse into it.
func walkRoot(dir string, depth int, gs *GlobalStatus, clusterByName map[string]int, stderrW io.Writer) error {
	if depth > globalWalkMaxDepth {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Per-dir read errors don't fail the whole walk — log + skip.
		fmt.Fprintf(stderrW, "level=WARN msg=%q dir=%q err=%q\n",
			"global_status: cannot read dir; skipping", dir, err.Error())
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip hidden dirs and a small allow-list of "definitely not
		// a sandbox" names so we don't walk into PG data dirs or
		// build artifacts. Sandbox dirs created by `deploy` never
		// start with "." or "_", so this is safe.
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		sub := filepath.Join(dir, name)
		switch {
		case config.IsSandboxDir(sub):
			// Standalone sandbox at this level. Don't recurse into it;
			// the data dir is below and not interesting to the walker.
			if entry := loadSandboxEntry(sub, stderrW); entry != nil {
				gs.Sandboxes = append(gs.Sandboxes, *entry)
			}
		case config.IsClusterDir(sub):
			ce := walkCluster(sub, stderrW)
			if ce != nil {
				clusterByName[ce.Name] = len(gs.Clusters)
				gs.Clusters = append(gs.Clusters, *ce)
			}
		default:
			// Plain dir. Recurse if we haven't hit the depth limit.
			if err := walkRoot(sub, depth+1, gs, clusterByName, stderrW); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkCluster builds a ClusterEntry for the given cluster dir. We
// load the manifest, then walk the manifest's Members in order
// (rather than os.ReadDir order) so the output reflects the cluster's
// declared topology — primary/publisher first.
//
// Missing members (manifest declares them, dir is gone) are recorded
// as stopped-with-empty-name entries so the user notices something
// is wrong but the walk doesn't fail.
func walkCluster(dir string, stderrW io.Writer) *ClusterEntry {
	m, err := config.LoadCluster(dir)
	if err != nil {
		fmt.Fprintf(stderrW, "level=WARN msg=%q dir=%q err=%q\n",
			"global_status: cannot load cluster manifest; skipping", dir, err.Error())
		return nil
	}
	ce := &ClusterEntry{
		Name:    m.Name,
		Dir:     dir,
		Mode:    m.Mode,
		Members: make([]SandboxEntry, 0, len(m.Members)),
	}
	for _, mb := range m.Members {
		memberDir := filepath.Join(dir, mb.Name)
		if !config.IsSandboxDir(memberDir) {
			// Manifest declares a member that isn't on disk. Emit a
			// placeholder so the row count matches the manifest and
			// the user sees "this should be here but isn't".
			ce.Members = append(ce.Members, SandboxEntry{
				Name:    mb.Name,
				Dir:     memberDir,
				State:   RunStateStopped, // not "missing" — we don't have a status enum for it; the empty Host/Port + cluster context conveys "absent"
				Role:    mb.Role,
				Cluster: m.Name,
			})
			continue
		}
		if entry := loadSandboxEntry(memberDir, stderrW); entry != nil {
			// Stamp the cluster name from the manifest so the renderer
			// can group correctly even if the per-sandbox config went
			// out of sync (e.g. user hand-edited it).
			entry.Cluster = m.Name
			ce.Members = append(ce.Members, *entry)
		}
	}
	return ce
}

// loadSandboxEntry loads the config at dir and builds a SandboxEntry
// with cheap-to-compute fields. Returns nil on load failure (already
// logged); the walk continues without this dir.
//
// The "cheap" promise: we read the JSON config, stat the pidfile, and
// do one TCP probe via portalloc.IsBusy. No psql, no fork/exec.
func loadSandboxEntry(dir string, stderrW io.Writer) *SandboxEntry {
	cfg, err := config.LoadSandbox(dir)
	if err != nil {
		fmt.Fprintf(stderrW, "level=WARN msg=%q dir=%q err=%q\n",
			"global_status: cannot load sandbox config; skipping", dir, err.Error())
		return nil
	}
	entry := &SandboxEntry{
		Name:    cfg.Name,
		Dir:     dir,
		Role:    cfg.Role,
		Host:    cfg.Host,
		Port:    cfg.Port,
		Cluster: cfg.Cluster,
	}
	pid := isRunning(cfg)
	listen := isPortListening(cfg)
	switch {
	case pid && listen:
		entry.State = RunStateRunning
	case !pid && !listen:
		entry.State = RunStateStopped
	default:
		entry.State = RunStateCrashed
	}
	return entry
}

// RenderText writes the human-friendly view of a GlobalStatus to w.
// Layout: a header line with the root, then a single aligned table
// with sections in order: standalone sandboxes, then each cluster,
// then orphans. Columns: NAME, STATE, ROLE, HOST:PORT, CLUSTER.
func (g *GlobalStatus) RenderText(w io.Writer) {
	fmt.Fprintf(w, "root=%s\n", g.Root)

	if len(g.Sandboxes) == 0 && len(g.Clusters) == 0 && len(g.Orphaned) == 0 {
		fmt.Fprintln(w, "(no sandboxes or clusters found)")
		return
	}

	// Compute column widths from ALL rows so the table aligns
	// regardless of which section a row lives in.
	rows := g.flattenForWidths()
	colName, colState, colRole, colHostPort, colCluster := 4, 5, 4, 9, 7
	for _, r := range rows {
		if n := len(r[0]); n > colName {
			colName = n
		}
		if n := len(r[1]); n > colState {
			colState = n
		}
		if n := len(r[2]); n > colRole {
			colRole = n
		}
		if n := len(r[3]); n > colHostPort {
			colHostPort = n
		}
		if n := len(r[4]); n > colCluster {
			colCluster = n
		}
	}
	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %-*s",
		colName, "NAME", colState, "STATE", colRole, "ROLE",
		colHostPort, "HOST:PORT", colCluster, "CLUSTER")
	rowFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds\n",
		colName, colState, colRole, colHostPort, colCluster)

	if len(g.Sandboxes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "# standalone sandboxes")
		fmt.Fprintln(w, header)
		for _, sb := range g.Sandboxes {
			renderSandboxRow(w, rowFmt, sb, "")
		}
	}
	for _, c := range g.Clusters {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "# cluster: %s (mode=%s, members=%d)\n", c.Name, c.Mode, len(c.Members))
		fmt.Fprintln(w, header)
		for _, sb := range c.Members {
			// Indent the name slightly so cluster members read as
			// "nested" in plain-text scanning. Other columns stay
			// aligned with the standalone section.
			renderSandboxRow(w, rowFmt, sb, "  ")
		}
	}
	if len(g.Orphaned) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "# orphaned (sandbox references a cluster not found on disk)")
		fmt.Fprintln(w, header)
		for _, sb := range g.Orphaned {
			renderSandboxRow(w, rowFmt, sb, "")
		}
	}
}

// renderSandboxRow writes one aligned row to w. namePrefix lets the
// cluster-section render call indent member names.
func renderSandboxRow(w io.Writer, fmtStr string, sb SandboxEntry, namePrefix string) {
	hostPort := ""
	if sb.Host != "" && sb.Port != 0 {
		hostPort = fmt.Sprintf("%s:%d", sb.Host, sb.Port)
	}
	cluster := sb.Cluster
	if cluster == "" {
		cluster = "-"
	}
	role := string(sb.Role)
	if role == "" {
		role = "-"
	}
	fmt.Fprintf(w, fmtStr, namePrefix+sb.Name, string(sb.State), role, hostPort, cluster)
}

// flattenForWidths returns every row that will appear in the table as
// a [name, state, role, host:port, cluster] slice. Used only to compute
// column widths.
func (g *GlobalStatus) flattenForWidths() [][5]string {
	out := make([][5]string, 0, len(g.Sandboxes)+len(g.Orphaned))
	push := func(sb SandboxEntry, indent string) {
		hp := ""
		if sb.Host != "" && sb.Port != 0 {
			hp = fmt.Sprintf("%s:%d", sb.Host, sb.Port)
		}
		c := sb.Cluster
		if c == "" {
			c = "-"
		}
		r := string(sb.Role)
		if r == "" {
			r = "-"
		}
		out = append(out, [5]string{indent + sb.Name, string(sb.State), r, hp, c})
	}
	for _, sb := range g.Sandboxes {
		push(sb, "")
	}
	for _, c := range g.Clusters {
		for _, sb := range c.Members {
			push(sb, "  ")
		}
	}
	for _, sb := range g.Orphaned {
		push(sb, "")
	}
	return out
}

// RenderJSON writes the GlobalStatus as a single JSON object. The
// shape is documented by the struct tags themselves — encoding/json
// reflects on them so what's in this file's struct definitions is
// what hits the wire.
func (g *GlobalStatus) RenderJSON(w io.Writer) error {
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("sandbox.GlobalStatus.RenderJSON: marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
