// CLI wiring for `pg_sandbox build`. SPEC §7.1.
//
// The heavy lifting (download, configure, make, contrib install) is
// in internal/build. This file owns:
//
//   - Flag parsing (--with-icu, --with-openssl, --configure-opts,
//     --jobs/-j, --force/-f).
//   - PGS_BIN_DIR / PGS_BUILD_DIR resolution (same layered chain as
//     every other command: flag → env → global config → built-in).
//   - SIGINT plumbing — long-running compiler must die when the
//     user Ctrl-Cs.
//   - Printing the install prefix on stdout, structured "built" line
//     on stderr.
//
// Output discipline (SPEC §4.6):
//   - stdout: ONLY the install prefix on success (machine-consumable).
//   - stderr: progress, debug, errors.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/guriandoro/postgresql_sandbox/go/internal/build"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// runBuild is the dispatcher contract for `build`.
func runBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		withICU       bool
		withOpenSSL   bool
		configureOpts string
		jobs          int
		force         bool
		binDir        string
		buildDir      string
	)
	fs.BoolVar(&withICU, "with-icu", false, "Pass --with-icu to configure")
	fs.BoolVar(&withOpenSSL, "with-openssl", false, "Pass --with-openssl to configure")
	fs.StringVar(&configureOpts, "configure-opts", "", "Extra ./configure flags (whitespace-split, NOT shell-parsed)")
	fs.IntVar(&jobs, "jobs", 0, "Parallelism for make (default: runtime.NumCPU())")
	fs.IntVar(&jobs, "j", 0, "Alias for --jobs")
	fs.BoolVar(&force, "force", false, "Overwrite an existing install prefix")
	fs.BoolVar(&force, "f", false, "Alias for --force")
	fs.StringVar(&binDir, "bin-dir", "", "Install root (each version goes under <bin-dir>/<version>/). Default $PGS_BIN_DIR, global defaultBinDir, or /opt/postgresql.")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&buildDir, "build-dir", "", "Build scratch dir. Default $PGS_BUILD_DIR or $TMPDIR/pg_sandbox-build/")

	// Reorder bool flags ahead of positionals so `build 18.4 --force`
	// works. See parseSubcommandArgs in argv.go for the full rationale
	// and the structural reason this is a single call rather than a
	// two-step pattern.
	if err := parseSubcommandArgs(fs, args); err != nil {
		return ui.ExitUsage.Int()
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "pg_sandbox build: <version> is required (e.g. 18.4)")
		usageHint(stderr, "build")
		return ui.ExitUsage.Int()
	}
	if len(rest) > 1 {
		fmt.Fprintln(stderr, "pg_sandbox build: only one version may be built at a time")
		return ui.ExitUsage.Int()
	}
	version := rest[0]

	// Layered resolution for bin-dir: flag → PGS_BIN_DIR → global
	// config → /opt/postgresql. Per SPEC §3.1 the explicit flag wins.
	// The helper also filepath.Abs's the result so the install
	// prefix we print on stdout is always absolute.
	binDir, err := resolveBinDir(binDir, loadGlobalConfig())
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox build: %v\n", err)
		return ui.ExitGeneric.Int()
	}

	if buildDir == "" {
		buildDir = os.Getenv("PGS_BUILD_DIR")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := build.Build(ctx, build.Options{
		Version:            version,
		BinDir:             binDir,
		BuildDir:           buildDir,
		WithICU:            withICU,
		WithOpenSSL:        withOpenSSL,
		ExtraConfigureOpts: configureOpts,
		Jobs:               jobs,
		Force:              force,
	}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pg_sandbox build: %v\n", err)
		var be *build.BuildError
		if errors.As(err, &be) {
			return be.ExitCode.Int()
		}
		return ui.ExitBuildFailed.Int()
	}
	// SPEC §4.6: stdout is for machine-consumable output. Print just
	// the install prefix so users can `$(pg_sandbox build 18.4)/bin`.
	fmt.Fprintln(stdout, res.InstallPrefix)
	return ui.ExitOK.Int()
}

// buildHelp prints `pg_sandbox help build`. SPEC §7.1.
func buildHelp(w io.Writer) {
	fmt.Fprintln(w, "pg_sandbox build — compile a PostgreSQL version from source")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pg_sandbox build <version> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Downloads, configures, builds, and installs PostgreSQL <version> (e.g. 18.4)")
	fmt.Fprintln(w, "under <bin-dir>/<version>/. Prints the install prefix on stdout so it can be")
	fmt.Fprintln(w, "captured: `BIN=$(pg_sandbox build 18.4)`.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	writeHelpFlags(w, []helpFlag{
		{"-b, --bin-dir <dir>", "Install root (default $PGS_BIN_DIR, global defaultBinDir, or /opt/postgresql)"},
		{"    --build-dir <dir>", "Build scratch dir (default $PGS_BUILD_DIR or $TMPDIR/pg_sandbox-build/)"},
		{"    --with-icu", "Pass --with-icu to configure"},
		{"    --with-openssl", "Pass --with-openssl to configure"},
		{"    --configure-opts <s>", "Extra ./configure flags (whitespace-split, NOT shell-parsed)"},
		{"-j, --jobs <n>", "Parallelism for make (default: runtime.NumCPU())"},
		{"-f, --force", "Overwrite an existing install prefix"},
	})
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  pg_sandbox build 18.4")
	fmt.Fprintln(w, "  pg_sandbox build 17.2 --with-icu --with-openssl -j 8")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See SPEC.md §7.1; docs/examples.md has a source-build recipe.")
}
