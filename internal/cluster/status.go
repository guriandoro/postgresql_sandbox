// Cluster status: walk every member declared in the manifest and
// report each one's per-sandbox status. SPEC §6.11 `cluster status`.
//
// The cluster-status output is the consolidation of N per-member
// sandbox.Status reports plus the cluster manifest metadata. We
// expose two render modes:
//
//   - Text (default): one "name=…" section per member, key=value
//     style matching the per-sandbox `status` output. Members appear
//     in manifest order (primary first, then standbys/subscribers in
//     ascending index).
//
//   - JSON (--json): a single object with cluster-level keys at the
//     top level and a "members" map keyed by member name. Each
//     member's value is the same StatusReport struct sandbox.Status
//     returns — so a script that parses cluster status can reuse the
//     same downstream parser it uses for per-sandbox status.
//
// Missing members (manifest declares them, but the dir is gone) are
// reported as state=missing rather than failing the whole command —
// SPEC framing of status as diagnostic ("a partial report is more
// useful than no report") applies at the cluster level too.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
)

// StatusOptions captures the inputs to `cluster status`.
type StatusOptions struct {
	// ClusterDir is the cluster to report on.
	ClusterDir string
}

// MemberStatus pairs a manifest entry with its live status report (or
// nil when the member's dir is missing). The Missing flag exists so
// the renderer can distinguish "probe failed inside Status" (rare)
// from "dir is gone" (common during partial destroy).
type MemberStatus struct {
	// Name is the member's identifier — copied from the manifest so
	// the renderer can index by it even when Report is nil.
	Name string `json:"name"`

	// Role is the manifest-declared role. Useful when Report is nil
	// (we still want to tell the user "this was the primary").
	Role config.Role `json:"role"`

	// Missing is true when the member dir doesn't contain a sandbox
	// (manifest says it should). State is reported as "missing" in
	// the text view; JSON consumers can branch on this flag.
	Missing bool `json:"missing"`

	// Report is the per-member sandbox.Status output. nil when
	// Missing is true or when LoadSandbox failed.
	Report *sandbox.StatusReport `json:"report,omitempty"`
}

// ClusterStatus is the consolidated report shape returned by Status.
// Carries the manifest's high-level metadata plus a per-member list.
type ClusterStatus struct {
	Name    string             `json:"name"`
	Mode    config.ClusterMode `json:"mode"`
	Members []MemberStatus     `json:"members"`
	Repl    config.ClusterRepl `json:"replication"`
	// Manifest carries the full manifest for JSON consumers that
	// want CreatedAt / SchemaVersion. Not rendered in the text view.
	Manifest *config.ClusterManifest `json:"manifest,omitempty"`
}

// Status loads the cluster manifest and fans out to sandbox.Status
// for each member. Returns a populated ClusterStatus even when some
// members are missing (state=missing in the text view).
func Status(ctx context.Context, runner pgexec.Runner, opts StatusOptions, stderrW io.Writer) (*ClusterStatus, error) {
	if opts.ClusterDir == "" {
		return nil, wrapExit(ExitUsage,
			fmt.Errorf("cluster.Status: ClusterDir is required"))
	}
	m, err := loadClusterOrFail(opts.ClusterDir)
	if err != nil {
		return nil, err
	}

	cs := &ClusterStatus{
		Name:     m.Name,
		Mode:     m.Mode,
		Repl:     m.Replication,
		Manifest: m,
		Members:  make([]MemberStatus, 0, len(m.Members)),
	}

	for _, member := range m.Members {
		dir := filepath.Join(opts.ClusterDir, member.Name)
		entry := MemberStatus{
			Name: member.Name,
			Role: member.Role,
		}
		if !config.IsSandboxDir(dir) {
			// Manifest declares it; dir is gone or never finished
			// deploying. Surface as missing and move on.
			entry.Missing = true
			cs.Members = append(cs.Members, entry)
			continue
		}
		rep, err := sandbox.StatusWithStderr(ctx, runner, dir, stderrW)
		if err != nil {
			// Log + continue: a single member failure isn't a cluster
			// status failure.
			fmt.Fprintf(stderrW,
				"level=WARN msg=%q member=%q err=%q\n",
				"cluster status: member status probe failed", member.Name, err.Error())
			entry.Missing = true
		} else {
			entry.Report = rep
		}
		cs.Members = append(cs.Members, entry)
	}
	return cs, nil
}

// RenderText writes a human-readable representation of the cluster
// status to w. Format: cluster header block, then one member block
// per member separated by a blank line. Each member block reuses
// sandbox.StatusReport.RenderText so the format matches `pg_sandbox
// status` byte-for-byte.
func (cs *ClusterStatus) RenderText(w io.Writer) {
	fmt.Fprintf(w, "cluster_name=%s\n", cs.Name)
	fmt.Fprintf(w, "cluster_mode=%s\n", cs.Mode)
	if cs.Repl.SlotPrefix != "" {
		fmt.Fprintf(w, "cluster_slot_prefix=%s\n", cs.Repl.SlotPrefix)
	}
	if cs.Repl.PublicationName != "" {
		fmt.Fprintf(w, "cluster_publication=%s\n", cs.Repl.PublicationName)
	}
	fmt.Fprintf(w, "cluster_sync_count=%d\n", cs.Repl.SyncCount)
	fmt.Fprintf(w, "cluster_members=%d\n", len(cs.Members))
	for _, m := range cs.Members {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "member=%s\n", m.Name)
		fmt.Fprintf(w, "member_role=%s\n", m.Role)
		if m.Missing {
			// SPEC §6.4 frames missing as a state, not an error. We
			// mirror sandbox.RenderText's vocabulary (running/
			// stopped/crashed/missing) so a parser handles all four
			// the same way.
			fmt.Fprintln(w, "state=missing")
			continue
		}
		if m.Report == nil {
			fmt.Fprintln(w, "state=unknown")
			continue
		}
		m.Report.RenderText(w)
	}
}

// RenderJSON writes the cluster status as a single JSON object. The
// shape is documented by the ClusterStatus + MemberStatus structs
// themselves — encoding/json reflects on the tags so what you see in
// schema.go and status.go's struct definitions is what hits the
// wire.
func (cs *ClusterStatus) RenderJSON(w io.Writer) error {
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return fmt.Errorf("cluster.RenderJSON: marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
