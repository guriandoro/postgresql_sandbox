// CLI wiring for `pg_sandbox publish`. SPEC §6.9.
//
// publish is a thin wrapper around sandbox.Publish: parse flags,
// load the sandbox config for its BinDir, hand off. Defaults follow
// the layered resolution chain (built-in → env → flag); --dbname is
// the only data-plane override here, the rest are required.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"strings"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/go/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runPublish implements the dispatcher contract for `publish`.
func runPublish(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		sandboxDir string
		pubName    string
		allTables  bool
		tablesCSV  string
		dbname     string
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.StringVar(&pubName, "pub-name", "", "Publication name (required)")
	fs.BoolVar(&allTables, "all-tables", false, "Publish FOR ALL TABLES (mutually exclusive with --tables)")
	fs.StringVar(&tablesCSV, "tables", "", "Comma-separated table list (mutually exclusive with --all-tables)")
	fs.StringVar(&dbname, "dbname", "", "Database to create the publication in (default: sandbox default)")
	fs.StringVar(&dbname, "d", "", "Alias for --dbname")

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	if sandboxDir == "" || pubName == "" {
		fmt.Fprintln(stderr, "pg_sandbox publish: --sandbox-dir and --pub-name are required")
		usageHint(stderr, "publish")
		return ui.ExitUsage.Int()
	}
	if allTables == (tablesCSV != "") {
		// Either both set or both unset; SPEC §6.9 mandates exactly
		// one. Surface as ExitUsage with a clear message rather than
		// letting it dribble down into sandbox.Publish's own check —
		// the user gets the right exit code immediately and doesn't
		// see flag-parser ambiguity.
		fmt.Fprintln(stderr, "pg_sandbox publish: exactly one of --all-tables or --tables is required")
		return ui.ExitUsage.Int()
	}
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox publish: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox publish: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	// Parse --tables into a slice. Empty strings between commas are
	// dropped (forgiving on the "foo,,bar" case); whitespace around
	// each name is trimmed.
	var tables []string
	if tablesCSV != "" {
		for _, t := range strings.Split(tablesCSV, ",") {
			if tt := strings.TrimSpace(t); tt != "" {
				tables = append(tables, tt)
			}
		}
		if len(tables) == 0 {
			fmt.Fprintln(stderr, "pg_sandbox publish: --tables parsed to no names")
			return ui.ExitUsage.Int()
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(cfg.BinDir)
	opts := sandbox.PublishOptions{
		SandboxDir: sandboxDir,
		PubName:    pubName,
		AllTables:  allTables,
		Tables:     tables,
		Dbname:     dbname,
	}
	if err := sandbox.Publish(ctx, runner, opts, stderr); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox publish: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	return ui.ExitOK.Int()
}

// publishHelp prints `pg_sandbox help publish`. SPEC §6.9.
func publishHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox publish — create a logical replication publication")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox publish -s <dir> --pub-name <name> --all-tables [--dbname <db>]")
	fmt.Fprintln(w, "  pg_sandbox publish -s <dir> --pub-name <name> --tables <t1,t2,...> [--dbname <db>]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Issues CREATE PUBLICATION on the sandbox. Exactly one of --all-tables or")
	fmt.Fprintln(w, "--tables is required.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
		{"    --pub-name <name>", "Publication name (required)"},
		{"    --all-tables", "Publish FOR ALL TABLES (mutually exclusive with --tables)"},
		{"    --tables <csv>", "Comma-separated table list (mutually exclusive with --all-tables)"},
		{"-d, --dbname <name>", "Database to create the publication in (default: sandbox default)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.9.")
}
