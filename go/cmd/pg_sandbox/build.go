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
	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
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
	fs.StringVar(&binDir, "bin-dir", "", "Install root (each version goes under <bin-dir>/<version>/). Default $PGS_BIN_DIR.")
	fs.StringVar(&binDir, "b", "", "Alias for --bin-dir")
	fs.StringVar(&buildDir, "build-dir", "", "Build scratch dir. Default $PGS_BUILD_DIR or $TMPDIR/pg_sandbox-build/")

	// Pre-process argv so users can put bool flags (--force / -f /
	// --with-icu / --with-openssl) AFTER the positional <version>.
	// Go's stdlib `flag` stops at the first non-flag, so without this
	// step `build 17.3 --force` treats `--force` as a second positional
	// version and Parse either errors with "only one version may be
	// built at a time" or silently treats --force as the version name.
	// Only bool flags are reordered; --bin-dir / --build-dir / --jobs /
	// --configure-opts take values and must stay adjacent to them. See
	// argv.go for the full contract.
	//
	// Bool flag names are derived from the FlagSet so a new BoolVar
	// above doesn't silently re-introduce the original UX bug.
	args = reorderBoolFlags(args, boolFlagNames(fs))

	if err := fs.Parse(args); err != nil {
		return ui.ExitUsage.Int()
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "pg_sandbox build: <version> is required (e.g. 17.3)")
		fs.Usage()
		return ui.ExitUsage.Int()
	}
	if len(rest) > 1 {
		fmt.Fprintln(stderr, "pg_sandbox build: only one version may be built at a time")
		return ui.ExitUsage.Int()
	}
	version := rest[0]

	// Layered resolution for bin-dir: flag → PGS_BIN_DIR → global config.
	// Per SPEC §3.1 the explicit flag wins.
	var globalCfg *config.Global
	if gp, err := config.GlobalConfigPath(); err == nil {
		if g, gerr := config.LoadGlobal(gp); gerr == nil {
			globalCfg = g
		}
	}
	if binDir == "" {
		binDir = os.Getenv("PGS_BIN_DIR")
	}
	if binDir == "" && globalCfg != nil {
		binDir = globalCfg.DefaultBinDir
	}
	if binDir == "" {
		// Match SPEC §7.1's sketch: build's default install root is
		// /opt/postgresql/ when nothing else is set. Documented in
		// the help text.
		binDir = "/opt/postgresql"
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
	// the install prefix so users can `$(pg_sandbox build 17.3)/bin`.
	fmt.Fprintln(stdout, res.InstallPrefix)
	return ui.ExitOK.Int()
}
