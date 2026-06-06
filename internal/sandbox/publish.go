// Logical-replication publisher path (SPEC §6.9 `publish`).
//
// Publish creates a publication on an existing sandbox. Three pieces
// of cross-cutting work happen before the actual CREATE PUBLICATION:
//
//  1. wal_level check. Logical replication REQUIRES wal_level =
//     logical at instance startup; a primary that was deployed
//     standalone is at the initdb default `replica`. We check via
//     SHOW wal_level; if it's not 'logical' we ALTER SYSTEM and
//     restart the instance through our own Stop/Start (NOT
//     pg_ctl restart) so the port-drift defense from commit 7740675
//     stays in force — pg_ctl restart drops the -o args we baked in
//     at start time, which silently moves the sandbox onto port 5432.
//
//  2. max_replication_slots and max_wal_senders. Both default to 10
//     on PG 18, which is plenty for sandbox use. We only raise them
//     if a previous `config set` (or hand-edit) dropped them below
//     10. The "raise to 10" choice is deliberate: it matches the
//     vendor default and keeps the on-disk config minimal.
//
//  3. CREATE PUBLICATION itself. The CLI layer chooses between
//     FOR ALL TABLES and FOR TABLE T1, T2; we receive the verb shape
//     already pre-built (allTables bool + table list). Duplicate
//     publication names are an ExitPublicationFailed — we never
//     silently DROP+CREATE.
//
// Why publication state is NOT persisted to config.Sandbox:
// the brief's "Schema decisions" section forbids adding fields to
// Sandbox in this slice. Publications live durably in
// pg_publication; status time queries that catalog. No state needs
// to round-trip through our JSON file.

package sandbox

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
)

// PublishOptions captures every input that influences a publish
// operation. The CLI layer in cmd/pg_sandbox/publish.go populates
// this from flag parsing.
type PublishOptions struct {
	// SandboxDir is the absolute path of the publisher sandbox.
	SandboxDir string

	// PubName is the publication name. Required.
	PubName string

	// AllTables, when true, generates `FOR ALL TABLES`. Mutually
	// exclusive with Tables — the CLI layer enforces this; the
	// sandbox layer also rejects "both set" defensively.
	AllTables bool

	// Tables is the explicit list of tables to include in the
	// publication (verbatim, schema-qualified if the caller wants).
	// Mutually exclusive with AllTables.
	Tables []string

	// Dbname overrides the database the publication is created in.
	// Empty means use cfg.DefaultDatabase.
	Dbname string
}

// minReplicationSlots is the floor we raise max_replication_slots /
// max_wal_senders to when a sandbox has them set below this. It
// matches the PG 18 vendor default, so this only ever applies when
// a user has explicitly lowered them.
const minReplicationSlots = 10

// Publish implements SPEC §6.9. It ensures the sandbox is configured
// for logical replication (raising wal_level + slot/sender counts
// via ALTER SYSTEM + restart if needed), then issues CREATE
// PUBLICATION.
//
// Documented failure modes:
//
//   - ExitNotASandbox: target dir is not a sandbox.
//   - ExitUsage: required fields missing or AllTables+Tables both set.
//   - ExitPgctlFailed: the restart-for-wal_level failed.
//   - ExitPublicationFailed: CREATE PUBLICATION returned non-zero
//     (including the "publication already exists" path; we don't
//     silently re-create).
func Publish(ctx context.Context, runner pgexec.Runner, opts PublishOptions, stderrW io.Writer) error {
	if opts.SandboxDir == "" {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Publish: SandboxDir is required"))
	}
	if opts.PubName == "" {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Publish: --pub-name is required"))
	}
	if opts.AllTables && len(opts.Tables) > 0 {
		return wrapExit(ExitUsage,
			fmt.Errorf("sandbox.Publish: --all-tables and --tables are mutually exclusive"))
	}
	if !opts.AllTables && len(opts.Tables) == 0 {
		return wrapExit(ExitUsage,
			fmt.Errorf("sandbox.Publish: one of --all-tables or --tables is required"))
	}

	cfg, err := loadSandboxOrFail(opts.SandboxDir)
	if err != nil {
		return err
	}
	if !isRunning(cfg) || !isPortListening(cfg) {
		// CREATE PUBLICATION requires a connection. We surface this
		// via ExitPublicationFailed (the operation cannot proceed,
		// and ExitSourceUnreachable is reserved for the replication
		// source side — the publisher IS our target here).
		return wrapExit(ExitPublicationFailed,
			fmt.Errorf("sandbox %q is not running (port %d on %s); start it first",
				cfg.Name, cfg.Port, cfg.Host))
	}

	dbname := opts.Dbname
	if dbname == "" {
		dbname = cfg.DefaultDatabase
	}

	// Step 1: ensure wal_level + slot/sender settings, restart if
	// any of them changed.
	if err := ensureLogicalSettings(ctx, runner, cfg, opts.SandboxDir, stderrW); err != nil {
		return err
	}

	// Step 2: build and run CREATE PUBLICATION. We sanitize the
	// publication name like the slot sanitizer does for physical
	// slots (see commits 44e784e / 37a5fe4); the table list is
	// taken verbatim because users include schema qualification.
	pubName := sanitizeSQLIdentifier(opts.PubName)
	var stmt string
	if opts.AllTables {
		stmt = fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES;", pubName)
	} else {
		stmt = fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s;",
			pubName, strings.Join(opts.Tables, ", "))
	}

	fmt.Fprintf(stderrW, "level=INFO msg=%q pub=%q db=%q sandbox=%q\n",
		"creating publication", pubName, dbname, cfg.Name)
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", dbname,
		"-c", stmt,
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "psql CREATE PUBLICATION", res.Stderr)
		return wrapExit(ExitPublicationFailed,
			fmt.Errorf("CREATE PUBLICATION %s: exit=%d: %w",
				pubName, res.ExitCode, res.Err))
	}
	fmt.Fprintf(stderrW, "level=INFO msg=%q pub=%q sandbox=%q\n",
		"published", pubName, cfg.Name)
	return nil
}

// ensureLogicalSettings is the wal_level + slot/sender preflight for
// Publish. It queries the live settings, ALTERs SYSTEM if any need
// raising, and (if anything changed) restarts the instance via our
// own Stop+Start. We deliberately do NOT use `pg_ctl restart`: see
// commit 7740675 — pg_ctl restart rewrites postmaster.opts without
// the -o args we passed at the initial start, so the next postgres
// loses its -h/-p and falls back to compiled-in defaults. Going
// through Stop+Start lets lifecycle.Start re-supply -o.
func ensureLogicalSettings(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, sandboxDir string, stderrW io.Writer) error {
	needRestart := false

	walLevel := showSetting(ctx, runner, cfg, "wal_level")
	if walLevel == "" {
		return wrapExit(ExitPublicationFailed,
			fmt.Errorf("could not read wal_level via SHOW; instance reachable?"))
	}
	if walLevel != "logical" {
		fmt.Fprintf(stderrW, "level=INFO msg=%q current=%q\n",
			"raising wal_level to logical via ALTER SYSTEM", walLevel)
		if err := alterSystem(ctx, runner, cfg, "wal_level", "logical", stderrW); err != nil {
			return err
		}
		needRestart = true
	}

	for _, setting := range []string{"max_replication_slots", "max_wal_senders"} {
		current := showSetting(ctx, runner, cfg, setting)
		if current == "" {
			// Probe failure — best effort; we already wrote a warn
			// from showSetting. Skip the raise rather than fail the
			// whole publish.
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(current))
		if err != nil {
			// Not a number? Defensive: skip raising.
			continue
		}
		if n < minReplicationSlots {
			fmt.Fprintf(stderrW, "level=INFO msg=%q setting=%q current=%d target=%d\n",
				"raising replication setting via ALTER SYSTEM",
				setting, n, minReplicationSlots)
			if err := alterSystem(ctx, runner, cfg, setting,
				strconv.Itoa(minReplicationSlots), stderrW); err != nil {
				return err
			}
			needRestart = true
		}
	}

	if !needRestart {
		return nil
	}

	fmt.Fprintf(stderrW, "level=INFO msg=%q sandbox=%q\n",
		"restarting sandbox to apply wal_level=logical", cfg.Name)
	if err := Stop(ctx, runner, sandboxDir, stderrW); err != nil {
		return err
	}
	if err := Start(ctx, runner, sandboxDir, stderrW); err != nil {
		return err
	}
	return nil
}

// showSetting runs `psql -c "SHOW <name>;"` and returns the trimmed
// first line of stdout. Empty on any error (caller should treat as a
// probe failure).
func showSetting(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, name string) string {
	// Postgres rejects SHOW with a name that isn't a known GUC; we
	// don't need to escape the name because it's caller-fixed
	// (literal strings in this file).
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", "SHOW "+name+";",
	)
	if res.Err != nil || res.ExitCode != 0 {
		return ""
	}
	out := strings.TrimSpace(string(res.Stdout))
	if idx := strings.IndexByte(out, '\n'); idx >= 0 {
		out = out[:idx]
	}
	return out
}

// alterSystem runs `psql -c "ALTER SYSTEM SET <name> = '<value>';"`.
// Values are single-quoted; Postgres accepts both quoted and unquoted
// numeric values for integer GUCs, so quoting is safe across the
// settings we manage.
//
// Failure here wraps ExitPublicationFailed because the publish path
// is the only caller and the user-visible error is "couldn't get
// publication created".
func alterSystem(ctx context.Context, runner pgexec.Runner, cfg *config.Sandbox, name, value string, stderrW io.Writer) error {
	stmt := fmt.Sprintf("ALTER SYSTEM SET %s = '%s';", name, value)
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", cfg.Host,
		"-p", strconv.Itoa(cfg.Port),
		"-U", cfg.Superuser,
		"-d", cfg.DefaultDatabase,
		"-c", stmt,
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "psql ALTER SYSTEM", res.Stderr)
		return wrapExit(ExitPublicationFailed,
			fmt.Errorf("ALTER SYSTEM SET %s: exit=%d: %w", name, res.ExitCode, res.Err))
	}
	return nil
}
