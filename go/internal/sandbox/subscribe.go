// Logical-replication subscriber path (SPEC §6.10 `subscribe`).
//
// This file holds the Subscribe entry point used by both the
// standalone `subscribe` command (cmd/pg_sandbox/subscribe.go) and
// the `deploy --subscribe-to` path (deploy_subscriber.go). Factoring
// the body here keeps the two callers from diverging on how a
// subscription is created, validated, and recorded.
//
// Design choices worth flagging:
//
//   - We reuse the caller-supplied Runner for both publisher-side
//     (`pg_dump --schema-only`) and subscriber-side (`psql -c CREATE
//     SUBSCRIPTION`) calls, for the same reason deploy_standby.go
//     reuses its Runner: source and destination almost always share a
//     bin-dir, and a single Runner is what test Fakes assume so
//     assertions stay reliable. See deploy_standby.go's file-level
//     comment for the longer rationale.
//
//   - Subscriber connects to publisher as the publisher's superuser
//     (typically `postgres`). The initdb default pg_hba line
//     `host all all 127.0.0.1/32 trust` already permits this; no
//     pg_hba edit is needed for logical pub/sub (unlike the physical
//     standby flow, which needs the special `host replication`
//     keyword). See SPEC §11 q2 and the design-decisions section of
//     the implementation brief.
//
//   - --copy-schema runs `pg_dump --schema-only | psql` BEFORE
//     CREATE SUBSCRIPTION. This way the subscription's initial
//     copy_data step copies row data into tables that already exist;
//     without it, copy_data would error or be a no-op.
//
//   - CopyMode persisted in the Logical block is informational
//     (inspectability per SPEC §3.1.6). The actual wire-level setting
//     is the WITH (copy_data = ...) clause computed once at create
//     time.

package sandbox

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
)

// SubscribeOptions captures every input that influences a subscribe
// operation. The CLI layer in cmd/pg_sandbox/subscribe.go populates
// this from flag parsing; the deploy path constructs it programatically.
type SubscribeOptions struct {
	// SandboxDir is the absolute path of the subscriber sandbox. Must
	// already be a deployed sandbox.
	SandboxDir string

	// PublisherRef is the user-supplied name/path of the publisher.
	// Resolved via resolveSourceSandbox: bare name → sibling under
	// SandboxDir's parent; relative path → cwd; absolute → as-is.
	PublisherRef string

	// PubName is the publication on the publisher this subscription
	// attaches to. Required.
	PubName string

	// SubName is the CREATE SUBSCRIPTION identifier on this sandbox.
	// Empty defers to the default `<this-sandbox-basename>_sub`.
	SubName string

	// Dbname overrides the database on this sandbox the subscription
	// is created in. Empty means use cfg.DefaultDatabase.
	Dbname string

	// CopySchema, if true, runs `pg_dump --schema-only` from the
	// publisher into this sandbox's chosen database BEFORE creating
	// the subscription. This populates the table list so the
	// subscription's initial copy_data has somewhere to land.
	CopySchema bool

	// NoCopyData, if true, creates the subscription with
	// WITH (copy_data = false). Mutually informative with CopySchema
	// (--copy-schema && --no-copy-data leaves you with empty schema-
	// only tables, which is sometimes what users want for catch-up
	// scenarios).
	NoCopyData bool
}

// Subscribe creates a logical-replication subscription on the
// sandbox at opts.SandboxDir attached to the publisher referenced by
// opts.PublisherRef. It updates the sandbox's config to record the
// subscription (Role=subscriber, Logical block populated).
//
// Documented failure modes:
//
//   - ExitNotASandbox: target dir is not a sandbox.
//   - ExitUsage: required fields missing.
//   - ExitSourceUnreachable: publisher dir not resolvable, not
//     loadable, or not running.
//   - ExitSchemaCopyFailed: --copy-schema's pg_dump|psql pipe failed.
//   - ExitSubscriptionFailed: CREATE SUBSCRIPTION returned non-zero.
func Subscribe(ctx context.Context, runner pgexec.Runner, opts SubscribeOptions, stderrW io.Writer) error {
	if opts.SandboxDir == "" {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Subscribe: SandboxDir is required"))
	}
	if opts.PublisherRef == "" {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Subscribe: --from publisher is required"))
	}
	if opts.PubName == "" {
		return wrapExit(ExitUsage, fmt.Errorf("sandbox.Subscribe: --pub-name is required"))
	}

	// Step 1: load this sandbox's config. Non-sandbox dir → ExitNotASandbox.
	subCfg, err := loadSandboxOrFail(opts.SandboxDir)
	if err != nil {
		return err
	}

	// Step 2: resolve and load the publisher's config.
	pubDir, err := resolveSourceSandbox(opts.SandboxDir, opts.PublisherRef)
	if err != nil {
		return err
	}
	pubCfg, err := config.LoadSandbox(pubDir)
	if err != nil {
		return fmt.Errorf("sandbox.Subscribe: load publisher config: %w", err)
	}

	// Step 3: publisher must be running AND past recovery. The
	// port-listen + pidfile pair matches the physical-replication
	// preflight in deploy_standby.go; we don't run pg_is_in_recovery
	// here because logical pub/sub does work on a primary that has
	// just been promoted, and adding the check would force a psql
	// round-trip for every subscribe.
	if !isRunning(pubCfg) || !isPortListening(pubCfg) {
		return wrapExit(ExitSourceUnreachable,
			fmt.Errorf("publisher sandbox %q is not running (port %d on %s)",
				pubCfg.Name, pubCfg.Port, pubCfg.Host))
	}

	// Resolve defaulted fields.
	subName := opts.SubName
	if subName == "" {
		subName = filepath.Base(opts.SandboxDir) + "_sub"
	}
	subName = sanitizeSQLIdentifier(subName)

	subDbname := opts.Dbname
	if subDbname == "" {
		subDbname = subCfg.DefaultDatabase
	}
	pubDbname := opts.Dbname
	if pubDbname == "" {
		// SPEC §6.10 doesn't separate publisher- and subscriber-side
		// dbnames; --dbname applies to both, matching what users
		// expect when they have matching db names on both ends. If
		// the user has mismatched names, they need to deploy with
		// matching defaults or use config set on one side.
		pubDbname = pubCfg.DefaultDatabase
	}

	// Step 4 (optional): pg_dump --schema-only piped into psql.
	if opts.CopySchema {
		if err := copySchema(ctx, runner, pubCfg, subCfg, pubDbname, subDbname, stderrW); err != nil {
			return err
		}
	}

	// Step 5: build CONN and CREATE SUBSCRIPTION. We connect to the
	// publisher as its superuser; local + trust = no password
	// needed. See file-level comment for why this is safe.
	connStr := fmt.Sprintf("host=%s port=%d user=%s dbname=%s",
		pubCfg.Host, pubCfg.Port, pubCfg.Superuser, pubDbname)
	copyData := "true"
	if opts.NoCopyData {
		copyData = "false"
	}
	createSQL := fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION %s WITH (copy_data = %s);",
		subName, connStr, sanitizeSQLIdentifier(opts.PubName), copyData)

	fmt.Fprintf(stderrW, "level=INFO msg=%q sub=%q pub=%q publisher=%q\n",
		"creating subscription", subName, opts.PubName, pubCfg.Name)
	res := runner.Run(ctx, "psql",
		"-X", "-A", "-t",
		"-h", subCfg.Host,
		"-p", fmt.Sprintf("%d", subCfg.Port),
		"-U", subCfg.Superuser,
		"-d", subDbname,
		"-c", createSQL,
	)
	if res.Err != nil || res.ExitCode != 0 {
		emitStderr(stderrW, "psql CREATE SUBSCRIPTION", res.Stderr)
		return wrapExit(ExitSubscriptionFailed,
			fmt.Errorf("CREATE SUBSCRIPTION %s: exit=%d: %w",
				subName, res.ExitCode, res.Err))
	}

	// Step 6: update on-disk config. We record the user-supplied
	// publisher reference (not the resolved absolute path) so the
	// file stays portable across machine layouts.
	copyMode := config.CopyAll
	switch {
	case opts.CopySchema:
		copyMode = config.CopySchema
	case opts.NoCopyData:
		copyMode = config.CopyNone
	}
	subCfg.Role = config.RoleSubscriber
	subCfg.Logical = &config.Logical{
		SourceSandbox:    opts.PublisherRef,
		PublicationName:  opts.PubName,
		SubscriptionName: subName,
		CopyMode:         copyMode,
		TargetDatabase:   subDbname,
	}
	if err := config.Validate(subCfg); err != nil {
		return fmt.Errorf("sandbox.Subscribe: validate: %w", err)
	}
	if err := config.SaveSandbox(opts.SandboxDir, subCfg); err != nil {
		return fmt.Errorf("sandbox.Subscribe: save config: %w", err)
	}

	fmt.Fprintf(stderrW, "level=INFO msg=%q sub=%q pub=%q publisher=%q copy_mode=%q\n",
		"subscribed", subName, opts.PubName, pubCfg.Name, copyMode)
	_ = time.Now() // (no audit beyond LastModifiedAt at this layer)
	return nil
}

// copySchema runs `pg_dump --schema-only` against the publisher and
// pipes the resulting SQL into `psql` on the subscriber. Both calls
// go through the same Runner so test Fakes intercept the pair as
// FakeCalls with Method=Run / Method=RunWithStdin.
//
// We use pgexec.RunWithStdin for the psql side: it captures the SQL
// the test wants to assert on (FakeCall.Stdin) without launching a
// real process. In production, the Stdin is the live stdout of the
// previous pg_dump Result — there's no streaming pipe here, the dump
// is fully captured first. For schema-only dumps this is fine (DDL
// is tiny); for data dumps we'd need a real pipe, but logical
// replication does its own row copy via copy_data, so we never dump
// data through this path.
func copySchema(ctx context.Context, runner pgexec.Runner, pubCfg, subCfg *config.Sandbox, pubDbname, subDbname string, stderrW io.Writer) error {
	fmt.Fprintf(stderrW, "level=INFO msg=%q from_db=%q to_db=%q\n",
		"pg_dump --schema-only | psql", pubDbname, subDbname)
	dump := runner.Run(ctx, "pg_dump",
		"--schema-only",
		"-h", pubCfg.Host,
		"-p", fmt.Sprintf("%d", pubCfg.Port),
		"-U", pubCfg.Superuser,
		"-d", pubDbname,
	)
	if dump.Err != nil || dump.ExitCode != 0 {
		emitStderr(stderrW, "pg_dump", dump.Stderr)
		return wrapExit(ExitSchemaCopyFailed,
			fmt.Errorf("pg_dump --schema-only: exit=%d: %w", dump.ExitCode, dump.Err))
	}
	// Apply via psql with stdin piped from the dump's stdout. We
	// pass -v ON_ERROR_STOP=1 so a single failing DDL statement
	// aborts the whole apply — partial schema application is worse
	// than a clean failure.
	apply := runner.RunWithStdin(ctx,
		strings.NewReader(string(dump.Stdout)),
		"psql",
		"-X",
		"-v", "ON_ERROR_STOP=1",
		"-h", subCfg.Host,
		"-p", fmt.Sprintf("%d", subCfg.Port),
		"-U", subCfg.Superuser,
		"-d", subDbname,
	)
	if apply.Err != nil || apply.ExitCode != 0 {
		emitStderr(stderrW, "psql (schema apply)", apply.Stderr)
		return wrapExit(ExitSchemaCopyFailed,
			fmt.Errorf("psql schema apply: exit=%d: %w", apply.ExitCode, apply.Err))
	}
	return nil
}

// sanitizeSQLIdentifier strips characters that would break an
// unquoted SQL identifier. Subscription / publication names are
// passed as bare identifiers in CREATE PUBLICATION / CREATE
// SUBSCRIPTION (no double-quoting), so non-identifier characters
// would either cause a syntax error or silently produce a different
// identifier than the user expects.
//
// Per pg_sandbox's pattern (see commits 44e784e / 37a5fe4 for the
// physical-slot equivalent), we replace anything not [a-zA-Z0-9_]
// with '_' and lowercase the result. This is deliberately
// non-injective — collisions are possible but rare and the user is
// in charge of supplying sensible names.
func sanitizeSQLIdentifier(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteRune('_')
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
