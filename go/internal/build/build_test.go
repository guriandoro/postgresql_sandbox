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
	"io"
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
	if be.ExitCode.Int() != 30 {
		t.Errorf("ExitCode = %d, want 30 (ExitBuildFailed)", be.ExitCode.Int())
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
