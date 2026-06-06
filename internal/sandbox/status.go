// Status reporting for a deployed sandbox. SPEC §6.4.
//
// Coverage in this iteration:
//
//   - Running state (running / stopped / crashed) from the pidfile +
//     port-listen pair.
//   - Server version via `psql -c "SELECT version()"`.
//   - Replication summary:
//       - Primary / unknown role → pg_stat_replication rows (one
//         per connected standby).
//       - Standby role → pg_stat_wal_receiver + pg_is_in_recovery().
//
// We use psql with -X -A -t -F'|' for the replication queries so
// the output is pipe-delimited and parseable with strings.Split. The
// "F" choice (pipe) is deliberately the same character the SPEC
// §6.4 sample output uses and is guaranteed not to appear in lsn /
// state / sync_state / lag-text values.
//
// Replication queries are best-effort: if any of them fail (extension
// missing, column rename across versions, source down), we log a
// warning to stderrW and continue. SPEC §6.4 frames status as
// diagnostic — a partial report is more useful than no report.
//
// The CLI layer renders the structured StatusReport as either
// key=value text (RenderText) or a JSON object (RenderJSON). The
// JSON schema is defined by the struct tags in this file.

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// RunState classifies the sandbox's runtime condition. The values
// are kept short so the human-table output reads cleanly.
type RunState string

const (
	// RunStateRunning: pidfile present AND something is listening on
	// the configured host:port. This is the healthy case.
	RunStateRunning RunState = "running"

	// RunStateStopped: no pidfile, no listener. Clean shutdown.
	RunStateStopped RunState = "stopped"

	// RunStateCrashed: pidfile present but no listener (or no
	// pidfile but listener — unlikely but covered). Indicates a
	// crash or a partial start. Status surfaces this so users see
	// it without having to read server.log first.
	RunStateCrashed RunState = "crashed"
)

// ReplicaRow is one row from pg_stat_replication, the primary's view
// of a connected standby.
type ReplicaRow struct {
	AppName   string `json:"app_name"`
	State     string `json:"state"`
	SyncState string `json:"sync_state"`
	WriteLag  string `json:"write_lag"`
	FlushLag  string `json:"flush_lag"`
	ReplayLag string `json:"replay_lag"`
}

// WalReceiverRow is the single row from pg_stat_wal_receiver on a
// standby. There is at most one row per standby (one WAL receiver
// per cluster), so the slice in StatusReport is conceptually
// optional rather than per-many.
//
// ReceiveStartLSN is the LSN at which the current WAL receiver
// stream started; the catalog column is `receive_start_lsn` from
// PG 10 onward (PG 18 dropped the legacy `received_lsn` alias).
type WalReceiverRow struct {
	Status          string `json:"status"`
	ReceiveStartLSN string `json:"receive_start_lsn"`
	WrittenLSN      string `json:"written_lsn"`
	FlushedLSN      string `json:"flushed_lsn"`
	LatestEndLSN    string `json:"latest_end_lsn"`
}

// SubscriptionRow combines pg_subscription (catalog) and
// pg_stat_subscription (live worker stats) for a subscriber. Both
// catalogs are queried in a single LEFT JOIN so a subscription with
// no live worker (subenabled=f) still reports its enabled state.
type SubscriptionRow struct {
	// Name is subname from pg_subscription.
	Name string `json:"name"`
	// Enabled mirrors pg_subscription.subenabled.
	Enabled bool `json:"enabled"`
	// PID of the worker; empty when subenabled=f or worker not yet
	// started.
	WorkerPID string `json:"worker_pid"`
	// ReceivedLSN is pg_stat_subscription.received_lsn.
	ReceivedLSN string `json:"received_lsn"`
	// LatestEndLSN is pg_stat_subscription.latest_end_lsn.
	LatestEndLSN string `json:"latest_end_lsn"`
	// LastMsgSendTime is pg_stat_subscription.last_msg_send_time.
	LastMsgSendTime string `json:"last_msg_send_time"`
}

// StatusReport is the structured form of `pg_sandbox status` output.
// The CLI layer chooses how to render this — text via RenderText,
// JSON via RenderJSON. The JSON shape is defined by the struct tags
// on this and its nested types; null vs empty-slice carries semantic
// meaning for Replicas and Publications (see field comments).
type StatusReport struct {
	Name     string      `json:"name"`
	State    RunState    `json:"state"`
	Role     config.Role `json:"role,omitempty"`
	Host     string      `json:"host"`
	Port     int         `json:"port"`
	User     string      `json:"user"`
	Database string      `json:"database"`
	DataDir  string      `json:"data_dir"`
	LogFile  string      `json:"log_file"`

	// Version is the trimmed PostgreSQL version string, or empty if
	// the sandbox is stopped or version probing failed.
	Version string `json:"version,omitempty"`

	// Replicas is the parsed pg_stat_replication output. Populated
	// when Role is RolePrimary or RoleUnknown and the query
	// succeeded. nil otherwise (including for "no replicas
	// connected" — distinguish via len(Replicas) == 0). The JSON
	// encoding deliberately preserves null vs [] so consumers can
	// tell "probe failed" from "no connected replicas".
	Replicas []ReplicaRow `json:"replicas"`

	// InRecovery reflects pg_is_in_recovery(): true on a standby,
	// false on a primary. Only meaningful when Role is RoleStandby
	// and the query succeeded.
	InRecovery bool `json:"in_recovery"`

	// WalReceiver is the single-row pg_stat_wal_receiver snapshot
	// when this sandbox is a standby and the query succeeded. nil
	// otherwise (including for "no receiver active yet").
	WalReceiver *WalReceiverRow `json:"wal_receiver,omitempty"`

	// Publications is the list of pg_publication.pubname rows on
	// this sandbox. Populated for ANY role when the query succeeded
	// (a primary that's also a publisher reports both replicas AND
	// publications). nil when the probe failed; empty slice when the
	// query ran with no rows. Encoded the same way as Replicas
	// (null vs [] is meaningful).
	Publications []string `json:"publications"`

	// Subscription is the joined pg_subscription /
	// pg_stat_subscription snapshot for this sandbox. Populated
	// when the sandbox has a Logical config block AND the probe
	// succeeded with a row. nil otherwise.
	Subscription *SubscriptionRow `json:"subscription,omitempty"`
}

// Status loads the sandbox config and probes the instance's runtime
// state. Returns a populated StatusReport even when the instance is
// stopped (stopped is a state, not a failure — SPEC §6.4).
//
// runner is used to invoke psql for the version + replication
// probes; if the sandbox is not running, psql is not called.
func Status(ctx context.Context, runner pgexec.Runner, dir string) (*StatusReport, error) {
	return statusWithWriter(ctx, runner, dir, io.Discard)
}

// StatusWithStderr is Status plus a destination for warning lines
// emitted during best-effort replication probes. Callers (the CLI)
// pass their own stderr writer so users see why a probe was skipped.
func StatusWithStderr(ctx context.Context, runner pgexec.Runner, dir string, stderrW io.Writer) (*StatusReport, error) {
	return statusWithWriter(ctx, runner, dir, stderrW)
}

// statusWithWriter is the shared body of Status / StatusWithStderr.
// Splitting Status into two public entry points (rather than an
// optional vararg) keeps the existing call sites unchanged while
// letting the CLI opt into warnings.
func statusWithWriter(ctx context.Context, runner pgexec.Runner, dir string, stderrW io.Writer) (*StatusReport, error) {
	cfg, err := loadSandboxOrFail(dir)
	if err != nil {
		return nil, err
	}

	rep := &StatusReport{
		Name:     cfg.Name,
		Role:     cfg.Role,
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.Superuser,
		Database: cfg.DefaultDatabase,
		DataDir:  cfg.DataDir,
		LogFile:  cfg.LogFile,
	}

	pid := isRunning(cfg)
	listen := isPortListening(cfg)
	switch {
	case pid && listen:
		rep.State = RunStateRunning
	case !pid && !listen:
		rep.State = RunStateStopped
	default:
		// One of pidfile/listener is present without the other.
		// Either case is a partial state worth flagging.
		rep.State = RunStateCrashed
	}

	if rep.State == RunStateRunning {
		// Best-effort version probe. A failure here is logged
		// indirectly via the empty Version field; we don't fail
		// status overall because the user can still see all the
		// other fields, and `status` is supposed to be diagnostic.
		rep.Version = probeVersion(ctx, runner, cfg)

		// Replication probes — split by role so we don't run the
		// wrong query against the wrong instance.
		switch cfg.Role {
		case config.RoleStandby:
			probeStandbyReplication(ctx, runner, cfg, rep, stderrW)
		case config.RolePrimary, config.RoleUnknown:
			probePrimaryReplication(ctx, runner, cfg, rep, stderrW)
		}

		// Logical-replication probes. Publications are queried for
		// every role (a primary or unknown sandbox may also be a
		// publisher) so we don't miss them when Role wasn't bumped
		// to RolePublisher (we deliberately don't auto-bump Role at
		// publish-time — see publish.go's schema-state note).
		probePublications(ctx, runner, cfg, rep, stderrW)
		if cfg.Logical != nil {
			probeSubscription(ctx, runner, cfg, rep, stderrW)
		}
	}
	return rep, nil
}

// probeVersion runs `psql -X -A -t -c "SELECT version()"` and
// returns the trimmed first line. Empty on any failure.
//
// Flags:
//
//   - -X: ignore .psqlrc; we want deterministic output regardless
//     of the user's environment.
//   - -A: unaligned output, no padding.
//   - -t: tuples-only, no header/footer.
func probeVersion(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox) string {
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", "SELECT version()",
	)
	if res.Err != nil || res.ExitCode != 0 {
		return ""
	}
	out := strings.TrimSpace(string(res.Stdout))
	// psql with -A -t prints one line per row; we only asked for
	// one row, so the first non-empty line is the answer.
	if idx := strings.IndexByte(out, '\n'); idx >= 0 {
		out = out[:idx]
	}
	return out
}

// probePrimaryReplication queries pg_stat_replication and stores the
// parsed rows in rep.Replicas. Best-effort — on any failure we log
// to stderrW and leave Replicas nil so the renderer prints "(probe
// failed)" rather than a misleading "no replicas".
func probePrimaryReplication(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, rep *StatusReport, stderrW io.Writer) {
	// COALESCE the lag columns to '' so an empty value reads cleanly
	// rather than as the literal NULL token psql emits. Cast to text
	// so the interval type renders the same way across PG versions.
	const query = "SELECT application_name, state, sync_state, " +
		"COALESCE(write_lag::text, ''), " +
		"COALESCE(flush_lag::text, ''), " +
		"COALESCE(replay_lag::text, '') " +
		"FROM pg_stat_replication;"
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t", "-F", "|",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", query,
	)
	if res.Err != nil || res.ExitCode != 0 {
		fmt.Fprintf(stderrW, "level=WARN msg=%q exit=%d\n",
			"pg_stat_replication probe failed", res.ExitCode)
		emitStderr(stderrW, "psql pg_stat_replication", res.Stderr)
		return
	}
	// Empty stdout = "query ran, no rows". That's a valid "no
	// replicas connected" reading; we set Replicas to a non-nil
	// empty slice so the renderer can distinguish it from "probe
	// failed".
	rep.Replicas = []ReplicaRow{}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) < 6 {
			// Skip malformed line rather than fail the whole probe.
			continue
		}
		rep.Replicas = append(rep.Replicas, ReplicaRow{
			AppName:   fields[0],
			State:     fields[1],
			SyncState: fields[2],
			WriteLag:  fields[3],
			FlushLag:  fields[4],
			ReplayLag: fields[5],
		})
	}
}

// probeStandbyReplication queries pg_is_in_recovery() and
// pg_stat_wal_receiver. Best-effort: any failure is a warn-level
// stderr line and the corresponding StatusReport field stays at its
// zero value.
func probeStandbyReplication(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, rep *StatusReport, stderrW io.Writer) {
	// Confirm recovery state — useful when a user thinks they've
	// promoted but the config still says standby (or vice versa).
	recRes := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", "SELECT pg_is_in_recovery();",
	)
	if recRes.Err == nil && recRes.ExitCode == 0 {
		rep.InRecovery = strings.TrimSpace(string(recRes.Stdout)) == "t"
	} else {
		fmt.Fprintf(stderrW, "level=WARN msg=%q exit=%d\n",
			"pg_is_in_recovery probe failed", recRes.ExitCode)
		emitStderr(stderrW, "psql pg_is_in_recovery", recRes.Stderr)
	}

	// Column note: pre-PG 18, pg_stat_wal_receiver had `received_lsn`;
	// PG 18 renamed it to `receive_start_lsn` and dropped the
	// legacy name. We query `receive_start_lsn` because it's the
	// modern surface and degrades to a single warn-level line on
	// older PG versions (caught by the best-effort wrapper above).
	const query = "SELECT status, " +
		"COALESCE(receive_start_lsn::text, ''), " +
		"COALESCE(written_lsn::text, ''), " +
		"COALESCE(flushed_lsn::text, ''), " +
		"COALESCE(latest_end_lsn::text, '') " +
		"FROM pg_stat_wal_receiver;"
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t", "-F", "|",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", query,
	)
	if res.Err != nil || res.ExitCode != 0 {
		fmt.Fprintf(stderrW, "level=WARN msg=%q exit=%d\n",
			"pg_stat_wal_receiver probe failed", res.ExitCode)
		emitStderr(stderrW, "psql pg_stat_wal_receiver", res.Stderr)
		return
	}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		// No receiver active. Leave WalReceiver nil so the renderer
		// can print "(no wal receiver)" rather than empty fields.
		return
	}
	fields := strings.Split(out, "|")
	if len(fields) < 5 {
		return
	}
	rep.WalReceiver = &WalReceiverRow{
		Status:          fields[0],
		ReceiveStartLSN: fields[1],
		WrittenLSN:      fields[2],
		FlushedLSN:      fields[3],
		LatestEndLSN:    fields[4],
	}
}

// probePublications queries pg_publication and stores the names in
// rep.Publications. Best-effort — on failure we log to stderrW and
// leave Publications nil so the renderer can distinguish "probe
// failed" from "no publications".
//
// The query targets the sandbox's default database. Publications are
// per-database in Postgres; the SPEC §6.4 status output reports
// what's visible in the default db, which is what users expect when
// they ran `publish` without --dbname. Cross-db inspection is
// possible via `use --dbname ... -c "SELECT pubname FROM
// pg_publication;"`.
func probePublications(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, rep *StatusReport, stderrW io.Writer) {
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", "SELECT pubname FROM pg_publication ORDER BY pubname;",
	)
	if res.Err != nil || res.ExitCode != 0 {
		fmt.Fprintf(stderrW, "level=WARN msg=%q exit=%d\n",
			"pg_publication probe failed", res.ExitCode)
		emitStderr(stderrW, "psql pg_publication", res.Stderr)
		return
	}
	// Empty stdout = "query ran, no rows". Non-nil empty slice so the
	// renderer can tell "probed and found none" from "probe failed".
	rep.Publications = []string{}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		rep.Publications = append(rep.Publications, name)
	}
}

// probeSubscription queries pg_subscription LEFT JOIN
// pg_stat_subscription for the subscription this sandbox is
// configured to host (cfg.Logical.SubscriptionName). Best-effort —
// failure leaves rep.Subscription nil.
//
// The join is necessary because pg_stat_subscription only has a row
// when a worker is connected (subenabled=t AND not too early for the
// launcher to spawn one). Without LEFT JOIN, a disabled subscription
// would produce zero rows and look like "no subscription" to the
// renderer.
func probeSubscription(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, rep *StatusReport, stderrW io.Writer) {
	// COALESCE the stat columns to '' so a missing row degrades to
	// empty strings rather than the NULL literal.
	query := fmt.Sprintf(
		"SELECT s.subname, s.subenabled, "+
			"COALESCE(st.pid::text, ''), "+
			"COALESCE(st.received_lsn::text, ''), "+
			"COALESCE(st.latest_end_lsn::text, ''), "+
			"COALESCE(st.last_msg_send_time::text, '') "+
			"FROM pg_subscription s "+
			"LEFT JOIN pg_stat_subscription st ON st.subid = s.oid "+
			"WHERE s.subname = '%s';",
		cfg.Logical.SubscriptionName)
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t", "-F", "|",
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.Logical.TargetDatabase,
		"-c", query,
	)
	if res.Err != nil || res.ExitCode != 0 {
		fmt.Fprintf(stderrW, "level=WARN msg=%q exit=%d\n",
			"pg_subscription probe failed", res.ExitCode)
		emitStderr(stderrW, "psql pg_subscription", res.Stderr)
		return
	}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		// No subscription row matches the name. This is unexpected
		// (config says we have one) but recoverable — surface as a
		// warn rather than a hard error.
		fmt.Fprintf(stderrW, "level=WARN msg=%q sub=%q\n",
			"pg_subscription has no row for configured subscription",
			cfg.Logical.SubscriptionName)
		return
	}
	// pg_subscription is per-database, but the catalog rows are
	// global; we still narrow by subname above. There can be at most
	// one row matching the unique subname.
	if idx := strings.IndexByte(out, '\n'); idx >= 0 {
		out = out[:idx]
	}
	fields := strings.Split(out, "|")
	if len(fields) < 6 {
		return
	}
	rep.Subscription = &SubscriptionRow{
		Name:            fields[0],
		Enabled:         strings.TrimSpace(fields[1]) == "t",
		WorkerPID:       fields[2],
		ReceivedLSN:     fields[3],
		LatestEndLSN:    fields[4],
		LastMsgSendTime: fields[5],
	}
}

// RenderText writes a human-friendly key=value block to w.
// Deliberately not a table: stdout consumers (column-aware filters
// like `awk`) handle key=value better than fixed-width columns, and
// this format matches the rest of the tool's diagnostic style.
func (r *StatusReport) RenderText(w io.Writer) {
	fmt.Fprintf(w, "name=%s\n", r.Name)
	fmt.Fprintf(w, "state=%s\n", r.State)
	if r.Role != "" {
		fmt.Fprintf(w, "role=%s\n", r.Role)
	}
	fmt.Fprintf(w, "host=%s\n", r.Host)
	fmt.Fprintf(w, "port=%d\n", r.Port)
	fmt.Fprintf(w, "user=%s\n", r.User)
	fmt.Fprintf(w, "database=%s\n", r.Database)
	fmt.Fprintf(w, "data_dir=%s\n", r.DataDir)
	fmt.Fprintf(w, "log_file=%s\n", r.LogFile)
	if r.Version != "" {
		fmt.Fprintf(w, "version=%s\n", r.Version)
	}

	// Replication sub-section. Distinguishable for parsers via the
	// `replicas[i]=…`, `in_recovery=…`, and `wal_receiver=…` key
	// shapes.
	if r.Replicas != nil {
		if len(r.Replicas) == 0 {
			fmt.Fprintln(w, "replicas=(no connected replicas)")
		} else {
			for i, row := range r.Replicas {
				fmt.Fprintf(w,
					"replicas[%d]=app=%s state=%s sync=%s write_lag=%q flush_lag=%q replay_lag=%q\n",
					i, row.AppName, row.State, row.SyncState,
					row.WriteLag, row.FlushLag, row.ReplayLag)
			}
		}
	}
	if r.Role == config.RoleStandby {
		fmt.Fprintf(w, "in_recovery=%t\n", r.InRecovery)
		if r.WalReceiver != nil {
			rw := r.WalReceiver
			fmt.Fprintf(w,
				"wal_receiver=status=%s receive_start_lsn=%s written_lsn=%s flushed_lsn=%s latest_end_lsn=%s\n",
				rw.Status, rw.ReceiveStartLSN, rw.WrittenLSN, rw.FlushedLSN, rw.LatestEndLSN)
		} else {
			fmt.Fprintln(w, "wal_receiver=(no active receiver)")
		}
	}

	// Logical-replication sub-sections. Publications are listed for
	// any role (a primary can also publish). Subscription is listed
	// only when this sandbox is configured as a subscriber.
	if r.Publications != nil && len(r.Publications) > 0 {
		fmt.Fprintf(w, "publications=[%s]\n", strings.Join(r.Publications, ", "))
	}
	if r.Subscription != nil {
		s := r.Subscription
		fmt.Fprintf(w,
			"subscription=name=%s enabled=%t worker_pid=%s received_lsn=%s latest_end_lsn=%s last_msg_send_time=%q\n",
			s.Name, s.Enabled, s.WorkerPID, s.ReceivedLSN, s.LatestEndLSN, s.LastMsgSendTime)
	}
}

// RenderJSON writes the StatusReport as a single JSON object. The
// shape is documented by the struct tags on StatusReport and its
// nested types.
func (r *StatusReport) RenderJSON(w io.Writer) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("sandbox.StatusReport.RenderJSON: marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
