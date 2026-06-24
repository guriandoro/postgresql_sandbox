// CLI wiring for `pg_sandbox subscribe`. SPEC §6.10.
//
// subscribe is a thin wrapper around sandbox.Subscribe: parse flags,
// resolve the subscriber's BinDir from its config, hand off. The
// `--from` flag (the publisher reference) is interpreted by
// sandbox.Subscribe via resolveSourceSandbox — same shape as
// `--replicate-from` on deploy.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/pgexec"
	"github.com/guriandoro/postgresql_sandbox/internal/sandbox"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// runSubscribe implements the dispatcher contract for `subscribe`.
func runSubscribe(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscribe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globals := registerGlobalFlags(fs)

	var (
		sandboxDir string
		from       string
		pubName    string
		subName    string
		copySchema bool
		noCopyData bool
		dbname     string
	)
	fs.StringVar(&sandboxDir, "sandbox-dir", "", "Target sandbox directory (required)")
	fs.StringVar(&sandboxDir, "s", "", "Alias for --sandbox-dir")
	fs.StringVar(&from, "from", "", "Publisher sandbox name (or absolute path) (required)")
	fs.StringVar(&pubName, "pub-name", "", "Publication name on the publisher (required)")
	fs.StringVar(&subName, "sub-name", "", "Subscription name (default <this-sandbox-basename>_sub)")
	fs.BoolVar(&copySchema, "copy-schema", false, "Run pg_dump --schema-only from the publisher before CREATE SUBSCRIPTION")
	fs.BoolVar(&noCopyData, "no-copy-data", false, "Create subscription with WITH (copy_data = false)")
	fs.StringVar(&dbname, "dbname", "", "Database name on both ends (default: sandbox default)")
	fs.StringVar(&dbname, "d", "", "Alias for --dbname")

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}
	logger, _, gErr := globals.Resolve(stderr)
	if gErr != nil {
		fmt.Fprintln(stderr, gErr)
		return ui.ExitUsage.Int()
	}
	stderr = globals.WrapStderr(stderr)
	if sandboxDir == "" || from == "" || pubName == "" {
		fmt.Fprintln(stderr, "pg_sandbox subscribe: --sandbox-dir, --from, and --pub-name are required")
		usageHint(stderr, "subscribe")
		return ui.ExitUsage.Int()
	}
	sandboxDir = resolveSandboxArg(sandboxDir, loadGlobalConfig())
	if !config.IsSandboxDir(sandboxDir) {
		fmt.Fprintf(stderr, "pg_sandbox subscribe: not a sandbox: %s\n", sandboxDir)
		return ui.ExitNotASandbox.Int()
	}
	cfg, err := config.LoadSandbox(sandboxDir)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox subscribe: load config: %v\n", err)
		return ui.ExitBadConfig.Int()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := pgexec.New(cfg.BinDir).WithLogger(logger)
	opts := sandbox.SubscribeOptions{
		SandboxDir:   sandboxDir,
		PublisherRef: from,
		PubName:      pubName,
		SubName:      subName,
		Dbname:       dbname,
		CopySchema:   copySchema,
		NoCopyData:   noCopyData,
	}
	if err := sandbox.Subscribe(ctx, runner, opts, stderr); err != nil {
		fmt.Fprintf(stderr, "pg_sandbox subscribe: %v\n", err)
		return sandbox.ExitCodeFor(err).Int()
	}
	return ui.ExitOK.Int()
}

// subscribeHelp prints `pg_sandbox help subscribe`. SPEC §6.10.
func subscribeHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox subscribe — create a logical replication subscription")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox subscribe -s <dir> --from <publisher> --pub-name <name> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Issues CREATE SUBSCRIPTION on the sandbox, pointing at <publisher>. With")
	fmt.Fprintln(w, "--copy-schema, pg_dump --schema-only is run from the publisher first so the")
	fmt.Fprintln(w, "subscription can succeed against a fresh sandbox.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-s, --sandbox-dir <dir>", "Target sandbox directory (required)"},
		{"    --from <ref>", "Publisher sandbox name (or absolute path) (required)"},
		{"    --pub-name <name>", "Publication name on the publisher (required)"},
		{"    --sub-name <name>", "Subscription name (default <this-sandbox-basename>_sub)"},
		{"    --copy-schema", "Run pg_dump --schema-only from the publisher before CREATE SUBSCRIPTION"},
		{"    --no-copy-data", "Create subscription with WITH (copy_data = false)"},
		{"-d, --dbname <name>", "Database name on both ends (default: sandbox default)"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  --from: a bare publisher name resolves as a SIBLING of this sandbox (its")
	fmt.Fprintln(w, "  parent dir), not under sandboxRoot; absolute paths are used as-is. See §5.2.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §6.10.")
}
