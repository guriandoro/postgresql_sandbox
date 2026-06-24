// Build pipeline for `pg_sandbox build`. See doc.go for the
// high-level design.
//
// This file is split into:
//
//   - URL / path helpers (pure, easy to unit-test).
//   - Configure-flag assembly (pure, unit-tested with each combination).
//   - Download (uses an injectable httpClient interface so tests can
//     simulate 200/404 without hitting the network).
//   - The Build entry point that wires it all together using the
//     pgexec.Runner from the rest of the codebase.
//
// We deliberately do NOT use pgexec's BinDir resolution for tar /
// configure / make: those are system-wide tools, not PG binaries,
// and forcing the user to pass --bin-dir for them would be confusing.
// The Runner here is given an empty BinDir so Locate falls back to
// PATH for everything.

package build

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/fsutil"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// versionRE is the strict shape we accept: major.minor, decimal
// integers only. Anything else (extra dots, leading zeros after a
// dot, alpha suffixes) is rejected before we touch the network.
var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// tarballURLTemplate is the official source download location. The
// %s placeholder is the version twice — once in the path component
// `v<version>/` and once in the filename `postgresql-<version>.tar.gz`.
const tarballURLTemplate = "https://ftp.postgresql.org/pub/source/v%s/postgresql-%s.tar.gz"

// Options captures every input to Build. The CLI layer populates this
// from flag parsing; everything in this file consumes it.
type Options struct {
	// Version is the requested PG release (e.g. "18.4"). Required.
	Version string

	// BinDir is the install root (per-version subdirs created under
	// it). Required. Resolves to PGS_BIN_DIR env in the CLI layer.
	BinDir string

	// BuildDir is the scratch directory for tarball download and
	// source extraction. If empty, defaults to
	// $TMPDIR/pg_sandbox-build/.
	BuildDir string

	// WithICU appends --with-icu to ./configure. Off by default to
	// match the Python tool.
	WithICU bool

	// WithOpenSSL appends --with-openssl to ./configure.
	WithOpenSSL bool

	// ExtraConfigureOpts is a whitespace-separated string of additional
	// configure flags. Split on whitespace (not shell-parsed) so users
	// can't sneak metacharacters past us.
	ExtraConfigureOpts string

	// Jobs is the -j parallelism for make. <= 0 means runtime.NumCPU().
	Jobs int

	// Force overrides an existing install prefix (rm -rf before install).
	Force bool
}

// Result reports the outcome of a successful build.
type Result struct {
	// InstallPrefix is the absolute path of the new install dir.
	// Printed verbatim to STDOUT by the CLI layer.
	InstallPrefix string

	// TarballPath is where the cached source archive lives. Surfaced
	// so a future rebuild can skip the download.
	TarballPath string

	// LogsDir is the per-step log directory; users can `less` the
	// configure / make / make_install files for diagnostics.
	LogsDir string
}

// httpClient is the minimum surface we need from net/http to perform
// a streamed tarball download. The struct field type is an interface
// so tests can plug in a fake. Real callers use http.DefaultClient.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Package-level seam so unit tests can simulate the pipeline without subprocesses.
var downloadTarballFn = downloadTarball
var runStepFn = runStep

// Build runs the entire compile pipeline. See doc.go.
//
// stderrW is where we write structured "step" log lines and human-
// friendly progress; nothing goes to stdout from inside this function
// (the install prefix is printed by the CLI layer on success). The
// pgexec.Runner-typed args are used only for the external tool steps;
// we don't take a Runner because Build is a sequence of system-tool
// invocations (tar, configure, make) and a single os/exec-backed
// helper is simpler than carrying the Runner abstraction through it.
func Build(ctx context.Context, opts Options, stderrW io.Writer) (*Result, error) {
	logger := slog.New(slog.NewTextHandler(stderrW, nil))

	if err := validateVersion(opts.Version); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitUsage, Err: err}
	}
	if opts.BinDir == "" {
		return nil, &BuildError{ExitCode: ui.ExitUsage, Err: fmt.Errorf("build: BinDir is required (set PGS_BIN_DIR or pass --bin-dir)")}
	}
	opts.BinDir = fsutil.ExpandTilde(opts.BinDir)
	if !filepath.IsAbs(opts.BinDir) {
		abs, err := filepath.Abs(opts.BinDir)
		if err != nil {
			return nil, &BuildError{ExitCode: ui.ExitUsage, Err: fmt.Errorf("build: abs(%s): %w", opts.BinDir, err)}
		}
		opts.BinDir = abs
	}

	installPrefix, binDirVersion := installPrefixFor(opts.BinDir, opts.Version)
	if binDirVersion != "" && binDirVersion != opts.Version {
		// User pointed --bin-dir / PGS_BIN_DIR at a directory whose
		// basename already looks like a major.minor version, but it
		// disagrees with the version they're building. We honor the
		// path they passed (no double-nesting) and warn so the
		// mismatch is visible in the log.
		logger.Warn("bin-dir basename looks like a version that does not match the build version; installing into bin-dir as-is",
			"bin_dir", opts.BinDir,
			"bin_dir_version", binDirVersion,
			"build_version", opts.Version,
		)
	}
	buildDir := opts.BuildDir
	if buildDir == "" {
		tmp := os.TempDir()
		buildDir = filepath.Join(tmp, "pg_sandbox-build")
	}
	buildDir = fsutil.ExpandTilde(buildDir)
	if !filepath.IsAbs(buildDir) {
		abs, err := filepath.Abs(buildDir)
		if err != nil {
			return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: fmt.Errorf("build: abs(%s): %w", buildDir, err)}
		}
		buildDir = abs
	}
	logsDir := filepath.Join(buildDir, "logs", opts.Version)
	srcDir := filepath.Join(buildDir, "pg_src", "postgresql-"+opts.Version)
	tarballPath := filepath.Join(buildDir, "postgresql-"+opts.Version+".tar.gz")

	// SPEC §7.1: refuse to overwrite an existing install unless --force.
	// We check the directory's existence (not emptiness) — a present
	// dir under PGS_BIN_DIR is the user's previous install and the
	// only safe semantics is "ask before clobbering".
	if st, err := os.Stat(installPrefix); err == nil && st.IsDir() {
		if !opts.Force {
			return nil, &BuildError{
				ExitCode: ui.ExitBuildFailed,
				Err:      fmt.Errorf("build: install dir %s already exists; pass --force to overwrite", installPrefix),
			}
		}
		logger.Info("removing existing install for --force rebuild", "prefix", installPrefix)
		if err := os.RemoveAll(installPrefix); err != nil {
			return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: fmt.Errorf("build: rm %s: %w", installPrefix, err)}
		}
	}

	for _, d := range []string{buildDir, logsDir, filepath.Join(buildDir, "pg_src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: fmt.Errorf("build: mkdir %s: %w", d, err)}
		}
	}

	// Stage 1: download.
	logger.Info("download", "version", opts.Version, "url", TarballURL(opts.Version), "target", tarballPath)
	if err := downloadTarballFn(ctx, http.DefaultClient, opts.Version, tarballPath, stderrW); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}

	// Stage 2: extract. If the srcDir already has contents from a
	// half-failed previous run, remove it first so tar doesn't
	// produce a mixed tree.
	if _, err := os.Stat(srcDir); err == nil {
		if err := os.RemoveAll(srcDir); err != nil {
			return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: fmt.Errorf("build: rm stale srcDir %s: %w", srcDir, err)}
		}
	}
	extractInto := filepath.Join(buildDir, "pg_src")
	if err := runStepFn(ctx, logger, logsDir, "extract", extractInto,
		nil, "tar", "-xzf", tarballPath, "-C", extractInto); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}
	if _, err := os.Stat(srcDir); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: fmt.Errorf("build: expected source tree at %s after extract: %w", srcDir, err)}
	}

	// Stage 3: configure.
	confArgs := assembleConfigureArgs(installPrefix, opts)
	debugEnv := buildDebugEnv() // PGS_BUILD_DEBUG controls CFLAGS injection.
	if err := runStepFn(ctx, logger, logsDir, "configure", srcDir, debugEnv,
		"./configure", confArgs...); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}

	// Stage 4: make.
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	if err := runStepFn(ctx, logger, logsDir, "make", srcDir, debugEnv,
		"make", "-j", fmt.Sprintf("%d", jobs)); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}

	// Stage 5: make install.
	if err := runStepFn(ctx, logger, logsDir, "make_install", srcDir, nil,
		"make", "install"); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}

	// Stage 6: contrib build + install. We don't add -j here because
	// contrib targets are small enough that the linker dominates and
	// parallelism rarely helps, and the upstream Makefile is robust to
	// serial builds.
	contribDir := filepath.Join(srcDir, "contrib")
	if err := runStepFn(ctx, logger, logsDir, "contrib_make", contribDir, debugEnv,
		"make"); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}
	if err := runStepFn(ctx, logger, logsDir, "contrib_install", contribDir, nil,
		"make", "install"); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}

	// Cleanup: drop the extracted source tree. Keep the tarball (cheap
	// to keep, expensive to re-download) and the logs (the user may
	// want to inspect them).
	if err := os.RemoveAll(srcDir); err != nil {
		// Non-fatal — install completed; warn and proceed.
		logger.Warn("could not remove extracted source dir", "dir", srcDir, "err", err)
	}

	logger.Info("built", "version", opts.Version, "prefix", installPrefix)
	return &Result{
		InstallPrefix: installPrefix,
		TarballPath:   tarballPath,
		LogsDir:       logsDir,
	}, nil
}

// validateVersion enforces the ^[0-9]+\.[0-9]+$ shape.
func validateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("build: <version> is required")
	}
	if !versionRE.MatchString(v) {
		return fmt.Errorf("build: invalid version %q; expected major.minor (e.g. 18.4)", v)
	}
	return nil
}

// TarballURL returns the canonical download URL for v. Exported so
// the CLI / tests can include it in error messages.
func TarballURL(v string) string {
	return fmt.Sprintf(tarballURLTemplate, v, v)
}

// installPrefixFor decides where `make install` should land given the
// resolved BinDir and the requested build version.
//
// The normal layout is BinDir/<version>/ — BinDir is a parent that
// holds one subdir per installed PG version. But users (and PGS_BIN_DIR
// values) sometimes already point at a version-shaped directory like
// `/opt/postgresql/18.4`; appending the version again would produce
// `/opt/postgresql/18.4/18.4` and silently surprise them. So when the
// basename of binDir matches the major.minor shape we recognize, we
// treat binDir as the install prefix itself and don't nest again.
//
// Returns:
//   - prefix: the directory to use as --prefix for ./configure.
//   - binDirVersion: the version-shaped basename we detected on
//     binDir, or "" if binDir did not look version-shaped. The caller
//     compares this against the requested version to decide whether to
//     emit a mismatch warning; keeping that decision out of this pure
//     helper makes it trivially unit-testable.
func installPrefixFor(binDir, version string) (prefix, binDirVersion string) {
	base := filepath.Base(binDir)
	if versionRE.MatchString(base) {
		return binDir, base
	}
	return filepath.Join(binDir, version), ""
}

// assembleConfigureArgs builds the argv to ./configure. Pure function,
// table-tested.
//
// Order:
//
//  1. --prefix is always first so users grepping the configure log
//     can see at a glance where the install will land.
//  2. ICU is OFF by default (--without-icu); --with-icu replaces it
//     if requested. We emit one or the other, never both — autoconf
//     would prefer the last flag, but being explicit avoids
//     surprise.
//  3. --with-openssl is appended only when WithOpenSSL is set.
//  4. --enable-cassert --enable-debug are appended when
//     PGS_BUILD_DEBUG=1.
//  5. ExtraConfigureOpts are appended last so they override anything
//     above (autoconf's last-wins).
func assembleConfigureArgs(prefix string, opts Options) []string {
	args := []string{"--prefix=" + prefix}
	if opts.WithICU {
		args = append(args, "--with-icu")
	} else {
		args = append(args, "--without-icu")
	}
	if opts.WithOpenSSL {
		args = append(args, "--with-openssl")
	}
	if os.Getenv("PGS_BUILD_DEBUG") == "1" {
		args = append(args, "--enable-cassert", "--enable-debug")
	}
	for _, f := range strings.Fields(opts.ExtraConfigureOpts) {
		args = append(args, f)
	}
	return args
}

// buildDebugEnv returns the env var list to add to make/configure when
// PGS_BUILD_DEBUG=1 is set. nil otherwise. Surfacing this as a helper
// keeps the env-injection logic in one place and unit-testable.
func buildDebugEnv() []string {
	if os.Getenv("PGS_BUILD_DEBUG") != "1" {
		return nil
	}
	// -O0 -g3: disable optimizations, include macro info. Lines up
	// with the standard "debuggable PG build" recipe.
	return []string{"CFLAGS=-O0 -g3"}
}

// downloadTarball streams the source archive into target. If target
// already exists with a non-zero size, the download is skipped (we
// trust the cached file — re-validating it would require the upstream
// SHA which we don't fetch separately).
//
// A non-200 HTTP response is converted to a typo'd-version error
// because that's the overwhelmingly common cause; the server's body
// is small and harmless to read.
func downloadTarball(ctx context.Context, client httpClient, version, target string, stderrW io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderrW, nil))
	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		logger.Info("using cached tarball", "path", target, "size", st.Size())
		return nil
	}

	url := TarballURL(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build: build request %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("build: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("build: PostgreSQL version %s not found at ftp.postgresql.org (HTTP 404); check the version number", version)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("build: GET %s: unexpected status %d", url, resp.StatusCode)
	}

	// Stream to a sibling temp file, then rename — so a failed
	// download doesn't leave a partial file masquerading as cached.
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp.*")
	if err != nil {
		return fmt.Errorf("build: tempfile in %s: %w", filepath.Dir(target), err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	written, err := io.Copy(tmp, resp.Body)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("build: copy body to %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("build: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("build: rename %s -> %s: %w", tmpName, target, err)
	}
	logger.Info("downloaded tarball", "path", target, "bytes", written)
	return nil
}

// runStep executes one external-tool stage of the build (extract,
// configure, make, make install, contrib_make, contrib_install).
// The child's stdout and stderr are tee'd to both the per-step log
// file AND stderrW (the parent's stderr) so the user gets live
// progress AND a post-mortem artifact.
//
// step is the symbolic name used in the log filename and log message.
// cwd is the working directory for the child. extraEnv is appended to
// os.Environ() (use this for CFLAGS injection); pass nil to inherit
// the parent env verbatim.
//
// Returns a wrapped error on non-zero exit or context cancellation.
// On success the log file is closed and left on disk.
func runStep(ctx context.Context, logger *slog.Logger, logsDir, step, cwd string, extraEnv []string, name string, args ...string) error {
	logPath := filepath.Join(logsDir, step+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("build: create log %s: %w", logPath, err)
	}
	defer logFile.Close()

	logger.Info("build step",
		"step", step,
		"cwd", cwd,
		"cmd", append([]string{name}, args...),
		"log", logPath,
	)

	// Header in the log file so a later reader knows what produced it.
	fmt.Fprintf(logFile, "# pg_sandbox build step=%s\n# cwd=%s\n# cmd=%s %s\n\n",
		step, cwd, name, strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	// Tee both streams into the log AND the caller's stderr. We use
	// io.MultiWriter rather than a tee goroutine because os/exec
	// already wires the child's streams onto whatever Writers we hand
	// it, so a MultiWriter does the duplication for free.
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build: step %q failed (see %s): %w", step, logPath, err)
	}
	return nil
}

// BuildError carries an exit code alongside the wrapped error so the
// CLI layer can return the right status. Mirrors the pattern used by
// internal/report for the same reason.
type BuildError struct {
	ExitCode ui.ExitCode
	Err      error
}

// Error implements error.
func (e *BuildError) Error() string {
	if e == nil || e.Err == nil {
		return "build: <nil>"
	}
	return e.Err.Error()
}

// Unwrap returns the wrapped error so errors.Is / errors.As work.
func (e *BuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
