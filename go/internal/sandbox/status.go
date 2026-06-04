// Status reporting for a deployed sandbox. SPEC §6.4.
//
// In this slice we cover the standalone-sandbox subset: running
// state (running / stopped / crashed), the server version (best
// effort via `psql -c "SELECT version()"`), and a small text table
// to stdout. Role-specific replication state (pg_stat_replication,
// pg_stat_subscription, etc.) is scoped to later slices.
//
// The --json flag is accepted as a no-op stub in this slice: when
// passed, we emit a single line indicating JSON output is deferred,
// and return ExitOK. Dropping the flag entirely would force a CLI
// breaking change later; accepting-it-as-stub keeps the surface
// stable.

package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
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

// StatusReport is the structured form of `pg_sandbox status` output.
// The CLI layer chooses how to render this (text table for default,
// JSON for --json once that lands).
type StatusReport struct {
	Name     string
	State    RunState
	Host     string
	Port     int
	User     string
	Database string
	DataDir  string
	LogFile  string

	// Version is the trimmed PostgreSQL version string, or empty if
	// the sandbox is stopped or version probing failed.
	Version string
}

// Status loads the sandbox config and probes the instance's runtime
// state. Returns a populated StatusReport even when the instance is
// stopped (stopped is a state, not a failure — SPEC §6.4).
//
// runner is used to invoke psql for the version probe; if the
// sandbox is not running, psql is not called.
func Status(ctx context.Context, runner pgexec.Runner, dir string) (*StatusReport, error) {
	cfg, err := loadSandboxOrFail(dir)
	if err != nil {
		return nil, err
	}

	rep := &StatusReport{
		Name:     cfg.Name,
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
		v := probeVersion(ctx, runner, cfg)
		rep.Version = v
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

// RenderText writes a human-friendly key=value block to w.
// Deliberately not a table: stdout consumers (column-aware filters
// like `awk`) handle key=value better than fixed-width columns, and
// this format matches the rest of the tool's diagnostic style.
func (r *StatusReport) RenderText(w io.Writer) {
	fmt.Fprintf(w, "name=%s\n", r.Name)
	fmt.Fprintf(w, "state=%s\n", r.State)
	fmt.Fprintf(w, "host=%s\n", r.Host)
	fmt.Fprintf(w, "port=%d\n", r.Port)
	fmt.Fprintf(w, "user=%s\n", r.User)
	fmt.Fprintf(w, "database=%s\n", r.Database)
	fmt.Fprintf(w, "data_dir=%s\n", r.DataDir)
	fmt.Fprintf(w, "log_file=%s\n", r.LogFile)
	if r.Version != "" {
		fmt.Fprintf(w, "version=%s\n", r.Version)
	}
}
