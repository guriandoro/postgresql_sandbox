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
	"crypto/sha256"
	"encoding/hex"
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
	"syscall"

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

// sha256HexRE matches exactly one lowercase/uppercase hex SHA-256
// digest. Used to validate the first field of the upstream .sha256
// file before we treat it as an expected checksum.
var sha256HexRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Options captures every input to Build. The CLI layer populates this
// from flag parsing; everything in this file consumes it.
type Options struct {
	// Version is the requested PG release (e.g. "18.4"). Required.
	Version string

	// BinDir is the install root (per-version subdirs created under
	// it). Required. Resolves to PGS_BIN_DIR env in the CLI layer.
	BinDir string

	// BuildDir is the scratch directory for tarball download and
	// source extraction. If empty, defaults to a per-user location:
	// os.UserCacheDir()/pg_sandbox/build (see defaultBuildDir for the
	// rationale and the fallback when UserCacheDir is unavailable).
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

// userCacheDirFn is a seam so tests can simulate os.UserCacheDir being
// unavailable (e.g. HOME unset) and exercise the fallback path.
var userCacheDirFn = os.UserCacheDir

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
	binDirVersionMismatch := binDirVersion != "" && binDirVersion != opts.Version
	if binDirVersionMismatch {
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
		var derr error
		buildDir, derr = defaultBuildDir()
		if derr != nil {
			return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: derr}
		}
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
		// MED-6: when the install prefix IS a version-shaped bin-dir whose
		// version disagrees with the build version, the existing directory
		// is a live install of a DIFFERENT PostgreSQL version (e.g. bin-dir
		// /opt/postgresql/16.4 while building 18.4). Wiping it — even with
		// --force — would destroy an unrelated install and drop the new
		// version into a directory misleadingly named after the old one.
		// Refuse outright, and do NOT steer the user toward --force.
		if binDirVersionMismatch {
			return nil, &BuildError{
				ExitCode: ui.ExitBuildFailed,
				Err: fmt.Errorf("build: refusing to overwrite existing install %s: its directory name is version %s but you are building %s — this looks like a live install of a different version. Point --bin-dir / PGS_BIN_DIR at a matching or neutral (non-version) path, or delete %s manually if you really mean to replace it",
					installPrefix, binDirVersion, opts.Version, installPrefix),
			}
		}
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

	// The build dir caches the tarball and holds the source tree we
	// compile and install, so it is created private (0o700) and — new
	// or pre-existing — verified to be ours and not writable by other
	// users before anything is downloaded into it (HIGH-3).
	if err := os.MkdirAll(buildDir, 0o700); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: fmt.Errorf("build: mkdir %s: %w", buildDir, err)}
	}
	if err := verifyBuildDirTrust(buildDir); err != nil {
		return nil, &BuildError{ExitCode: ui.ExitBuildFailed, Err: err}
	}
	// Sub-dirs are gated by the (verified) build dir itself, so plain
	// 0o755 is fine for them.
	for _, d := range []string{logsDir, filepath.Join(buildDir, "pg_src")} {
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

// ChecksumURL returns the URL of the upstream SHA-256 companion file
// that ftp.postgresql.org publishes next to each release tarball.
func ChecksumURL(v string) string {
	return TarballURL(v) + ".sha256"
}

// defaultBuildDir resolves the build scratch dir used when the user did
// not pass --build-dir / PGS_BUILD_DIR. We deliberately do NOT default
// to os.TempDir(): on multi-user Linux that is the world-writable /tmp,
// where any local user can pre-create the predictable
// /tmp/pg_sandbox-build/ path, own it, and feed us a poisoned tarball
// or swap the extracted source tree mid-build (HIGH-3).
// os.UserCacheDir (~/.cache on Linux, ~/Library/Caches on macOS) is
// per-user by construction and survives reboots, so the tarball cache
// keeps paying off across sessions. Only when UserCacheDir is
// unavailable (e.g. HOME unset) do we fall back to a fresh, randomly
// named 0o700 MkdirTemp dir — unpredictable, at the cost of losing the
// cache for that run.
func defaultBuildDir() (string, error) {
	if cache, err := userCacheDirFn(); err == nil {
		return filepath.Join(cache, "pg_sandbox", "build"), nil
	}
	dir, err := os.MkdirTemp("", "pg_sandbox-build-")
	if err != nil {
		return "", fmt.Errorf("build: no user cache dir and MkdirTemp fallback failed: %w", err)
	}
	return dir, nil
}

// verifyBuildDirTrust rejects a build dir that another local user could
// tamper with. The tarball cache and the extracted source tree live
// here and are subsequently compiled and installed, so a dir owned by
// someone else or writable by group/other is a supply-chain hole:
// whoever controls it controls the code we build. Called after MkdirAll
// so a pre-existing dir (the attack vector — e.g. a pre-created
// world-readable path) is checked rather than blindly reused.
//
// The ownership check relies on syscall.Stat_t, which is fine here:
// the tool targets Linux/macOS only (it already uses syscall.Exec).
func verifyBuildDirTrust(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("build: stat build dir %s: %w", dir, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("build: build dir %s is not a directory", dir)
	}
	if perm := st.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("build: build dir %s is group/world-writable (%04o); another user could tamper with the source we compile — chmod go-w it or pass a private --build-dir", dir, perm)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		if uid := os.Getuid(); int(sys.Uid) != uid {
			return fmt.Errorf("build: build dir %s is owned by uid %d, not the current user (uid %d); refusing to build from a directory another user controls — pass a private --build-dir", dir, sys.Uid, uid)
		}
	}
	return nil
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

// downloadTarball streams the source archive into target, verifying it
// against the upstream .sha256 companion file (HIGH-3).
//
// Checksum semantics:
//
//   - Fresh download: the .sha256 is fetched FIRST. On a match the
//     digest is stored next to the tarball (<target>.sha256, sha256sum
//     format) so later cache hits can verify offline. On a mismatch we
//     refuse and delete nothing but the temp file (nothing was cached).
//   - Cache reuse: a non-empty file at target is only trusted after
//     re-hashing it against the stored .sha256 (or, if a previous tool
//     version cached without one, against a freshly fetched .sha256).
//     A mismatch is a hard error telling the user to delete the cached
//     file and retry — we never silently rebuild from a file that
//     failed verification.
//   - Missing upstream .sha256 (HTTP 404 — releases predating the
//     companion files): we WARN loudly and proceed without
//     verification. Any other checksum-fetch failure (5xx, network
//     error) is a hard error: a transient hiccup must not silently
//     downgrade the security check.
//
// A non-200 HTTP response for the tarball itself is converted to a
// typo'd-version error because that's the overwhelmingly common cause;
// the server's body is small and harmless to read.
func downloadTarball(ctx context.Context, client httpClient, version, target string, stderrW io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderrW, nil))
	shaPath := target + ".sha256"

	// Cache-reuse path: never trust a pre-existing file without
	// re-hashing it.
	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		return verifyCachedTarball(ctx, client, version, target, shaPath, st.Size(), logger)
	}

	// Fresh download: expected digest first, then the tarball.
	expected, err := fetchChecksum(ctx, client, version)
	if err != nil {
		return err
	}
	if expected == "" {
		logger.Warn("upstream publishes no .sha256 for this version; proceeding WITHOUT checksum verification",
			"url", ChecksumURL(version))
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
	// The stream is hashed as it is written so verification needs no
	// second pass.
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp.*")
	if err != nil {
		return fmt.Errorf("build: tempfile in %s: %w", filepath.Dir(target), err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("build: copy body to %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("build: close %s: %w", tmpName, err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if expected != "" && got != expected {
		cleanup()
		return fmt.Errorf("build: downloaded tarball failed SHA-256 verification (got %s, upstream %s says %s); refusing to build — retry, and investigate the network path if it persists", got, ChecksumURL(version), expected)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("build: rename %s -> %s: %w", tmpName, target, err)
	}
	if expected != "" {
		if err := storeChecksum(shaPath, expected, filepath.Base(target)); err != nil {
			// Non-fatal: the tarball itself is verified; a missing
			// stored digest only means the next cache hit re-fetches
			// the .sha256 from upstream.
			logger.Warn("could not store checksum next to tarball", "path", shaPath, "err", err)
		}
		logger.Info("downloaded tarball", "path", target, "bytes", written, "sha256", got)
		return nil
	}
	logger.Info("downloaded tarball", "path", target, "bytes", written, "sha256", "UNVERIFIED (no upstream .sha256)")
	return nil
}

// verifyCachedTarball decides whether a pre-existing tarball at target
// may be reused. The expected digest comes from the stored .sha256
// sibling when present, or from upstream when a previous tool version
// cached the tarball without one (in which case the fetched digest is
// stored for next time). See downloadTarball for the full semantics.
func verifyCachedTarball(ctx context.Context, client httpClient, version, target, shaPath string, size int64, logger *slog.Logger) error {
	var expected string
	if raw, err := os.ReadFile(shaPath); err == nil {
		expected, err = parseChecksumDigest(string(raw))
		if err != nil {
			return fmt.Errorf("build: stored checksum %s is unusable (%v); delete %s and %s and retry", shaPath, err, target, shaPath)
		}
	} else {
		expected, err = fetchChecksum(ctx, client, version)
		if err != nil {
			return err
		}
		if expected == "" {
			logger.Warn("using cached tarball WITHOUT verification: no stored checksum and upstream publishes no .sha256 for this version",
				"path", target, "url", ChecksumURL(version))
			return nil
		}
	}
	got, err := fileSHA256(target)
	if err != nil {
		return fmt.Errorf("build: hash cached tarball %s: %w", target, err)
	}
	if got != expected {
		return fmt.Errorf("build: cached tarball %s failed SHA-256 verification (got %s, want %s); it may be corrupt or tampered with — delete %s and %s and retry", target, got, expected, target, shaPath)
	}
	if _, err := os.Stat(shaPath); err != nil {
		if err := storeChecksum(shaPath, expected, filepath.Base(target)); err != nil {
			logger.Warn("could not store checksum next to tarball", "path", shaPath, "err", err)
		}
	}
	logger.Info("using cached tarball", "path", target, "size", size, "sha256", "verified")
	return nil
}

// fetchChecksum GETs the upstream .sha256 companion for version and
// returns the expected hex digest. A 404 returns ("", nil) — the
// caller decides how loudly to proceed unverified (older releases
// predate the companion files). Any other failure is an error: we fail
// closed rather than let a transient hiccup skip verification.
func fetchChecksum(ctx context.Context, client httpClient, version string) (string, error) {
	url := ChecksumURL(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build: build request %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("build: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("build: GET %s: unexpected status %d", url, resp.StatusCode)
	}
	// The file is one sha256sum-format line (~100 bytes); cap the read
	// defensively anyway.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("build: read %s: %w", url, err)
	}
	digest, err := parseChecksumDigest(string(body))
	if err != nil {
		return "", fmt.Errorf("build: parse %s: %w", url, err)
	}
	return digest, nil
}

// parseChecksumDigest extracts the hex digest from sha256sum-format
// content ("<64 hex chars>  <filename>\n"; a bare digest is also
// accepted). Normalizes to lowercase.
func parseChecksumDigest(s string) (string, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum content")
	}
	digest := strings.ToLower(fields[0])
	if !sha256HexRE.MatchString(digest) {
		return "", fmt.Errorf("first field %q is not a SHA-256 hex digest", fields[0])
	}
	return digest, nil
}

// storeChecksum writes the digest next to the tarball in sha256sum
// format so a later `shasum -a 256 -c` by the user also works.
func storeChecksum(shaPath, digest, tarballName string) error {
	return os.WriteFile(shaPath, []byte(digest+"  "+tarballName+"\n"), 0o644)
}

// fileSHA256 returns the lowercase hex SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
