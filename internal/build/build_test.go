// Unit tests for the build package.
//
// The pipeline as a whole is exercised by the smoke test on the dev
// machine (it actually compiles a tarball). Here we cover the pure
// pieces (URL construction, version validation, configure-flag
// assembly) plus the download path against a fake httpClient that
// simulates 200 and 404 responses.

package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateVersion(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"17.3", true},
		{"16.4", true},
		{"18.2", true},
		{"9.6", true},
		{"", false},
		{"17", false},
		{"17.3.1", false},
		{"v17.3", false},
		{"17.3-rc1", false},
		{"abc", false},
		{"17.3 ", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			err := validateVersion(tc.in)
			got := err == nil
			if got != tc.want {
				t.Fatalf("validateVersion(%q) ok=%v want=%v (err=%v)", tc.in, got, tc.want, err)
			}
		})
	}
}

func TestTarballURL(t *testing.T) {
	got := TarballURL("17.3")
	want := "https://ftp.postgresql.org/pub/source/v17.3/postgresql-17.3.tar.gz"
	if got != want {
		t.Fatalf("TarballURL = %q want %q", got, want)
	}
}

func TestAssembleConfigureArgs(t *testing.T) {
	// Cover each toggle independently so a regression in any one is
	// localized. PGS_BUILD_DEBUG must NOT be set by the test
	// environment; we save/restore in subtests that toggle it.
	prevDebug := os.Getenv("PGS_BUILD_DEBUG")
	_ = os.Unsetenv("PGS_BUILD_DEBUG")
	t.Cleanup(func() {
		if prevDebug == "" {
			_ = os.Unsetenv("PGS_BUILD_DEBUG")
		} else {
			_ = os.Setenv("PGS_BUILD_DEBUG", prevDebug)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		got := assembleConfigureArgs("/opt/pg/17.3", Options{})
		want := []string{"--prefix=/opt/pg/17.3", "--without-icu"}
		assertArgs(t, got, want)
	})

	t.Run("with-icu", func(t *testing.T) {
		got := assembleConfigureArgs("/opt/pg/17.3", Options{WithICU: true})
		want := []string{"--prefix=/opt/pg/17.3", "--with-icu"}
		assertArgs(t, got, want)
	})

	t.Run("with-openssl", func(t *testing.T) {
		got := assembleConfigureArgs("/opt/pg/17.3", Options{WithOpenSSL: true})
		want := []string{"--prefix=/opt/pg/17.3", "--without-icu", "--with-openssl"}
		assertArgs(t, got, want)
	})

	t.Run("extra-opts splits on whitespace", func(t *testing.T) {
		got := assembleConfigureArgs("/p", Options{ExtraConfigureOpts: "  --with-llvm  --with-python "})
		want := []string{"--prefix=/p", "--without-icu", "--with-llvm", "--with-python"}
		assertArgs(t, got, want)
	})

	t.Run("PGS_BUILD_DEBUG adds debug flags", func(t *testing.T) {
		_ = os.Setenv("PGS_BUILD_DEBUG", "1")
		defer os.Unsetenv("PGS_BUILD_DEBUG")
		got := assembleConfigureArgs("/p", Options{})
		want := []string{"--prefix=/p", "--without-icu", "--enable-cassert", "--enable-debug"}
		assertArgs(t, got, want)
	})

	t.Run("all toggles together preserve order", func(t *testing.T) {
		_ = os.Setenv("PGS_BUILD_DEBUG", "1")
		defer os.Unsetenv("PGS_BUILD_DEBUG")
		got := assembleConfigureArgs("/p", Options{
			WithICU:            true,
			WithOpenSSL:        true,
			ExtraConfigureOpts: "--with-llvm",
		})
		want := []string{
			"--prefix=/p",
			"--with-icu",
			"--with-openssl",
			"--enable-cassert", "--enable-debug",
			"--with-llvm",
		}
		assertArgs(t, got, want)
	})
}

func TestBuildDebugEnv(t *testing.T) {
	prev := os.Getenv("PGS_BUILD_DEBUG")
	t.Cleanup(func() {
		if prev == "" {
			_ = os.Unsetenv("PGS_BUILD_DEBUG")
		} else {
			_ = os.Setenv("PGS_BUILD_DEBUG", prev)
		}
	})

	_ = os.Unsetenv("PGS_BUILD_DEBUG")
	if got := buildDebugEnv(); got != nil {
		t.Errorf("unset: got %v want nil", got)
	}

	_ = os.Setenv("PGS_BUILD_DEBUG", "1")
	got := buildDebugEnv()
	if len(got) != 1 || !strings.HasPrefix(got[0], "CFLAGS=") {
		t.Errorf("PGS_BUILD_DEBUG=1: got %v want [CFLAGS=...]", got)
	}

	_ = os.Setenv("PGS_BUILD_DEBUG", "true") // anything other than literal "1" is OFF
	if got := buildDebugEnv(); got != nil {
		t.Errorf("PGS_BUILD_DEBUG=true: got %v want nil (only literal \"1\" enables debug)", got)
	}
}

// fakeHTTP returns the configured response without making any
// real network calls.
type fakeHTTP struct {
	status int
	body   string
	err    error
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    req,
		Header:     make(http.Header),
	}, nil
}

func TestDownloadTarball_404(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-99.99.tar.gz")
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), &fakeHTTP{status: 404}, "99.99", target, &buf)
	if err == nil {
		t.Fatalf("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "99.99") {
		t.Errorf("error %q should mention the version", err.Error())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should say 'not found'", err.Error())
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("404 must not leave a tarball on disk; stat err=%v", statErr)
	}
}

func TestDownloadTarball_200(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	const body = "fake-tarball-bytes"
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), &fakeHTTP{status: 200, body: body}, "17.3", target, &buf)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read tarball: %v", err)
	}
	if string(data) != body {
		t.Errorf("body mismatch: got %q want %q", string(data), body)
	}
}

func TestDownloadTarball_cached(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	const cached = "cached-bytes"
	if err := os.WriteFile(target, []byte(cached), 0o644); err != nil {
		t.Fatal(err)
	}
	// httpClient that would fail if called — proves the cache path
	// returns without touching it.
	fc := &fakeHTTP{err: errors.New("must not call")}
	var buf bytes.Buffer
	if err := downloadTarball(context.Background(), fc, "17.3", target, &buf); err != nil {
		t.Fatalf("cached download: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != cached {
		t.Errorf("cached file overwritten: got %q want %q", string(data), cached)
	}
}

func TestInstallPrefixFor(t *testing.T) {
	tests := []struct {
		name        string
		binDir      string
		version     string
		wantPrefix  string
		wantVerSeen string
	}{
		{
			name:        "no version segment: append version",
			binDir:      "/opt/postgresql",
			version:     "18.4",
			wantPrefix:  "/opt/postgresql/18.4",
			wantVerSeen: "",
		},
		{
			name:        "binDir already ends in matching version: use as-is",
			binDir:      "/opt/postgresql/18.4",
			version:     "18.4",
			wantPrefix:  "/opt/postgresql/18.4",
			wantVerSeen: "18.4",
		},
		{
			name:        "binDir ends in mismatching version: use as-is, report basename for warning",
			binDir:      "/opt/postgresql/18.4",
			version:     "18.3",
			wantPrefix:  "/opt/postgresql/18.4",
			wantVerSeen: "18.4",
		},
		{
			name:        "basename only looks numeric but not major.minor: append (safe default)",
			binDir:      "/opt/postgresql/18",
			version:     "18.4",
			wantPrefix:  "/opt/postgresql/18/18.4",
			wantVerSeen: "",
		},
		{
			name:        "trailing slash is normalized by filepath.Base: detection still fires",
			binDir:      "/opt/postgresql/18.4/",
			version:     "18.4",
			wantPrefix:  "/opt/postgresql/18.4/",
			wantVerSeen: "18.4",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPrefix, gotVerSeen := installPrefixFor(tc.binDir, tc.version)
			if gotPrefix != tc.wantPrefix {
				t.Errorf("prefix: got %q want %q", gotPrefix, tc.wantPrefix)
			}
			if gotVerSeen != tc.wantVerSeen {
				t.Errorf("binDirVersion: got %q want %q", gotVerSeen, tc.wantVerSeen)
			}
		})
	}
}

func TestBuild_RejectsExistingInstall(t *testing.T) {
	bin := t.TempDir()
	build := t.TempDir()
	// Pre-create an "existing install".
	if err := os.MkdirAll(filepath.Join(bin, "17.3"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, err := Build(context.Background(), Options{
		Version:  "17.3",
		BinDir:   bin,
		BuildDir: build,
	}, &buf)
	if err == nil {
		t.Fatal("expected error when install dir exists without --force")
	}
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("want *BuildError, got %T", err)
	}
	// ExitBuildFailed is the documented code for this case.
	if be.ExitCode.Int() != 29 {
		t.Errorf("ExitCode = %d, want 29 (ExitBuildFailed)", be.ExitCode.Int())
	}
}

func TestBuild_RejectsBadVersion(t *testing.T) {
	var buf bytes.Buffer
	_, err := Build(context.Background(), Options{
		Version: "not-a-version",
		BinDir:  t.TempDir(),
	}, &buf)
	if err == nil {
		t.Fatal("expected error on bad version")
	}
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("want *BuildError, got %T", err)
	}
	if be.ExitCode.Int() != 2 {
		t.Errorf("ExitCode = %d, want 2 (ExitUsage)", be.ExitCode.Int())
	}
}

// TestBuild_VersionShapedBinDir covers the version-collision behavior end
// to end through Build(): a version-shaped BinDir basename means we do
// NOT nest the version under it, and a mismatch between that basename
// and the requested build version emits a WARN log line. Both subtests
// rely on the "install dir already exists" check firing on the resolved
// installPrefix to stop the pipeline before any network / compile work,
// which is exactly the assertion: if Build had appended the version
// again, the pre-created install dir would have been BinDir/<ver>/ and
// the check would not have triggered.
func TestBuild_VersionShapedBinDir(t *testing.T) {
	t.Run("matching version does not warn and uses bin-dir as install prefix", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "18.4")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		_, err := Build(context.Background(), Options{
			Version:  "18.4",
			BinDir:   bin,
			BuildDir: t.TempDir(),
		}, &buf)
		if err == nil {
			t.Fatal("expected error: existing install dir at bin-dir itself")
		}
		if !strings.Contains(err.Error(), bin) {
			t.Errorf("error should reference %q (the install prefix), got: %v", bin, err)
		}
		if strings.Contains(buf.String(), "level=WARN") {
			t.Errorf("did not expect a WARN line when basename matches build version; stderr=\n%s", buf.String())
		}
	})

	t.Run("mismatching version warns but still uses bin-dir as install prefix", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "18.4")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		_, err := Build(context.Background(), Options{
			Version:  "18.3",
			BinDir:   bin,
			BuildDir: t.TempDir(),
		}, &buf)
		if err == nil {
			t.Fatal("expected error: existing install dir at bin-dir itself")
		}
		if !strings.Contains(err.Error(), bin) {
			t.Errorf("error should reference %q (the install prefix), got: %v", bin, err)
		}
		out := buf.String()
		if !strings.Contains(out, "level=WARN") {
			t.Errorf("expected a WARN line; stderr=\n%s", out)
		}
		if !strings.Contains(out, "bin_dir_version=18.4") {
			t.Errorf("warn should include bin_dir_version=18.4; stderr=\n%s", out)
		}
		if !strings.Contains(out, "build_version=18.3") {
			t.Errorf("warn should include build_version=18.3; stderr=\n%s", out)
		}
	})
}

// swapBuildSeams replaces the package-level downloadTarballFn / runStepFn
// with the supplied test doubles for the duration of the current test.
// Restoration runs via t.Cleanup so the production functions are always
// reinstated, even when a subtest fails.
func swapBuildSeams(
	t *testing.T,
	dl func(ctx context.Context, client httpClient, version, target string, stderrW io.Writer) error,
	step func(ctx context.Context, logger *slog.Logger, logsDir, step, cwd string, extraEnv []string, name string, args ...string) error,
) {
	t.Helper()
	origDL, origStep := downloadTarballFn, runStepFn
	downloadTarballFn = dl
	runStepFn = step
	t.Cleanup(func() {
		downloadTarballFn = origDL
		runStepFn = origStep
	})
}

// fakeDownload writes a small placeholder tarball to target so the
// cached-tarball branch is exercised on retry and the file system
// reflects "download succeeded".
func fakeDownload(_ context.Context, _ httpClient, _ string, target string, _ io.Writer) error {
	return os.WriteFile(target, []byte("fake-tarball"), 0o644)
}

// makeExtractStep returns a runStepFn double that satisfies the
// post-extract os.Stat(srcDir) check by creating the expected source
// tree when step == "extract". failStep, when non-empty, identifies
// the symbolic step that should be reported as failing — the returned
// error matches the production wrapper shape from runStep itself so
// the assertion that the wrapped error mentions the step name and log
// path stays meaningful.
func makeExtractStep(buildDir, version, failStep string) func(context.Context, *slog.Logger, string, string, string, []string, string, ...string) error {
	srcDir := filepath.Join(buildDir, "pg_src", "postgresql-"+version)
	return func(_ context.Context, _ *slog.Logger, logsDir, step, _ string, _ []string, _ string, _ ...string) error {
		if step == failStep {
			return fmt.Errorf("build: step %q failed (see %s): boom", step, filepath.Join(logsDir, step+".log"))
		}
		if step == "extract" {
			if err := os.MkdirAll(srcDir, 0o755); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestBuild_ForceOverwritesExistingInstall(t *testing.T) {
	bin := t.TempDir()
	build := t.TempDir()
	prefix := filepath.Join(bin, "17.3")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(prefix, "sentinel-old.txt")
	if err := os.WriteFile(sentinel, []byte("from a previous install"), 0o644); err != nil {
		t.Fatal(err)
	}

	swapBuildSeams(t, fakeDownload, makeExtractStep(build, "17.3", ""))

	var buf bytes.Buffer
	res, err := Build(context.Background(), Options{
		Version:  "17.3",
		BinDir:   bin,
		BuildDir: build,
		Force:    true,
	}, &buf)
	if err != nil {
		t.Fatalf("Build with Force: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil Result on success")
	}
	if res.InstallPrefix != prefix {
		t.Errorf("InstallPrefix = %q, want %q", res.InstallPrefix, prefix)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sentinel still exists after --force; stat err=%v", err)
	}
}

func TestBuild_ForceOverwritesPopulatedInstall(t *testing.T) {
	bin := t.TempDir()
	build := t.TempDir()
	prefix := filepath.Join(bin, "17.3")
	// Populate the install dir with nested files to prove --force does a
	// recursive wipe, not just an empty-dir removal.
	nested := filepath.Join(prefix, "lib", "postgresql")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "stale.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	swapBuildSeams(t, fakeDownload, makeExtractStep(build, "17.3", ""))

	var buf bytes.Buffer
	res, err := Build(context.Background(), Options{
		Version:  "17.3",
		BinDir:   bin,
		BuildDir: build,
		Force:    true,
	}, &buf)
	if err != nil {
		t.Fatalf("Build with Force on populated install: %v", err)
	}
	if res == nil || res.InstallPrefix != prefix {
		t.Fatalf("unexpected Result: %#v", res)
	}
	if _, err := os.Stat(filepath.Join(nested, "stale.so")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("nested stale file survived --force; stat err=%v", err)
	}
}

func TestBuild_StepFailureMapsToBuildError(t *testing.T) {
	steps := []string{"extract", "configure", "make", "make_install", "contrib_make", "contrib_install"}
	for _, failing := range steps {
		failing := failing
		t.Run(failing, func(t *testing.T) {
			bin := t.TempDir()
			build := t.TempDir()
			swapBuildSeams(t, fakeDownload, makeExtractStep(build, "17.3", failing))

			var buf bytes.Buffer
			_, err := Build(context.Background(), Options{
				Version:  "17.3",
				BinDir:   bin,
				BuildDir: build,
			}, &buf)
			if err == nil {
				t.Fatalf("expected error when step %q fails", failing)
			}
			var be *BuildError
			if !errors.As(err, &be) {
				t.Fatalf("want *BuildError, got %T (%v)", err, err)
			}
			if be.ExitCode.Int() != 29 {
				t.Errorf("ExitCode = %d, want 29 (ExitBuildFailed)", be.ExitCode.Int())
			}
			if !strings.Contains(err.Error(), failing) {
				t.Errorf("error %q should mention failing step %q", err.Error(), failing)
			}
			expectLog := filepath.Join(build, "logs", "17.3", failing+".log")
			if !strings.Contains(err.Error(), expectLog) {
				t.Errorf("error %q should mention log path %q", err.Error(), expectLog)
			}
		})
	}
}

func TestBuild_SuccessReturnsExpectedPaths(t *testing.T) {
	bin := t.TempDir()
	build := t.TempDir()
	swapBuildSeams(t, fakeDownload, makeExtractStep(build, "17.3", ""))

	var buf bytes.Buffer
	res, err := Build(context.Background(), Options{
		Version:  "17.3",
		BinDir:   bin,
		BuildDir: build,
	}, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil Result")
	}
	wantPrefix := filepath.Join(bin, "17.3")
	wantTarball := filepath.Join(build, "postgresql-17.3.tar.gz")
	wantLogs := filepath.Join(build, "logs", "17.3")
	if res.InstallPrefix != wantPrefix {
		t.Errorf("InstallPrefix = %q, want %q", res.InstallPrefix, wantPrefix)
	}
	if res.TarballPath != wantTarball {
		t.Errorf("TarballPath = %q, want %q", res.TarballPath, wantTarball)
	}
	if res.LogsDir != wantLogs {
		t.Errorf("LogsDir = %q, want %q", res.LogsDir, wantLogs)
	}
	for _, p := range []string{res.InstallPrefix, res.TarballPath, res.LogsDir} {
		if !filepath.IsAbs(p) {
			t.Errorf("expected absolute path, got %q", p)
		}
	}
}

func TestBuild_VersionShapedBinDir_Success(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "18.4")
	build := t.TempDir()
	// Pre-create so the basename detection sees a real directory; --force
	// drives the overwrite path so Build proceeds to the stubbed steps.
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	swapBuildSeams(t, fakeDownload, makeExtractStep(build, "18.4", ""))

	var buf bytes.Buffer
	res, err := Build(context.Background(), Options{
		Version:  "18.4",
		BinDir:   bin,
		BuildDir: build,
		Force:    true,
	}, &buf)
	if err != nil {
		t.Fatalf("Build with version-shaped BinDir: %v", err)
	}
	if res.InstallPrefix != bin {
		t.Errorf("InstallPrefix = %q, want %q (bin-dir reused as-is)", res.InstallPrefix, bin)
	}
}

func TestBuild_RelativeBinDir_IsAbsoluted(t *testing.T) {
	// filepath.Abs resolves relative paths against the current working
	// directory, so anchor the test in a TempDir to keep the absolute
	// path predictable and isolated from whatever the test runner cwd is.
	rootDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(rootDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	relBin := "relative-bin"
	build := t.TempDir()
	swapBuildSeams(t, fakeDownload, makeExtractStep(build, "17.3", ""))

	var buf bytes.Buffer
	res, err := Build(context.Background(), Options{
		Version:  "17.3",
		BinDir:   relBin,
		BuildDir: build,
		Force:    true,
	}, &buf)
	if err != nil {
		t.Fatalf("Build with relative BinDir: %v", err)
	}
	if !filepath.IsAbs(res.InstallPrefix) {
		t.Errorf("InstallPrefix not absolute: %q", res.InstallPrefix)
	}
	if !strings.HasSuffix(res.InstallPrefix, filepath.Join(relBin, "17.3")) {
		t.Errorf("InstallPrefix = %q, want suffix %q", res.InstallPrefix, filepath.Join(relBin, "17.3"))
	}
}

// assertArgs compares two argv slices and fails the test with a
// readable diff when they differ.
func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv length: got %d (%v) want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]: got %q want %q\n got: %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}
