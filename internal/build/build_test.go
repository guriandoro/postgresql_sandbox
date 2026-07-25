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
	"crypto/sha256"
	"encoding/hex"
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

// fakeHTTP returns the configured response for every request without
// making any real network calls. Use routedHTTP when the checksum and
// tarball URLs must answer differently.
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

// fakeResponse is one canned answer for routedHTTP.
type fakeResponse struct {
	status int
	body   string
	err    error
}

// routedHTTP maps exact request URLs to canned responses and records
// the order of requests, so tests can drive the .sha256 and tarball
// fetches independently and assert which ones happened.
type routedHTTP struct {
	routes map[string]fakeResponse
	calls  []string
}

func (f *routedHTTP) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	f.calls = append(f.calls, url)
	r, ok := f.routes[url]
	if !ok {
		return nil, fmt.Errorf("routedHTTP: unexpected request %s", url)
	}
	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Request:    req,
		Header:     make(http.Header),
	}, nil
}

// sha256Hex returns the lowercase hex SHA-256 of s, for building
// matching .sha256 bodies in tests.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestDownloadTarball_404(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-99.99.tar.gz")
	var buf bytes.Buffer
	// Everything 404s: the .sha256 miss downgrades to a WARN, the
	// tarball miss is the "typo'd version" error.
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
	fc := &routedHTTP{routes: map[string]fakeResponse{
		ChecksumURL("17.3"): {status: 200, body: sha256Hex(body) + "  postgresql-17.3.tar.gz\n"},
		TarballURL("17.3"):  {status: 200, body: body},
	}}
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), fc, "17.3", target, &buf)
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
	// The verified digest must be stored next to the tarball so a
	// later cache hit can verify offline.
	sha, err := os.ReadFile(target + ".sha256")
	if err != nil {
		t.Fatalf("read stored .sha256: %v", err)
	}
	if !strings.Contains(string(sha), sha256Hex(body)) {
		t.Errorf("stored .sha256 %q should contain digest %s", string(sha), sha256Hex(body))
	}
	// The .sha256 must be fetched BEFORE the tarball (fail early, and
	// never leave an unverified file behind).
	if len(fc.calls) != 2 || fc.calls[0] != ChecksumURL("17.3") {
		t.Errorf("expected [.sha256, tarball] request order, got %v", fc.calls)
	}
}

func TestDownloadTarball_200_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	fc := &routedHTTP{routes: map[string]fakeResponse{
		ChecksumURL("17.3"): {status: 200, body: strings.Repeat("a", 64) + "  postgresql-17.3.tar.gz\n"},
		TarballURL("17.3"):  {status: 200, body: "tampered-bytes"},
	}}
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), fc, "17.3", target, &buf)
	if err == nil {
		t.Fatal("expected error on checksum mismatch")
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("error %q should mention SHA-256", err.Error())
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("mismatch must not leave a tarball on disk; stat err=%v", statErr)
	}
	// No stray temp files either.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty dir after mismatch, found %v", entries)
	}
}

func TestDownloadTarball_200_MissingUpstreamChecksum(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	const body = "fake-tarball-bytes"
	fc := &routedHTTP{routes: map[string]fakeResponse{
		ChecksumURL("17.3"): {status: 404},
		TarballURL("17.3"):  {status: 200, body: body},
	}}
	var buf bytes.Buffer
	if err := downloadTarball(context.Background(), fc, "17.3", target, &buf); err != nil {
		t.Fatalf("download with missing upstream .sha256: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != body {
		t.Errorf("body mismatch: got %q want %q", string(data), body)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "WITHOUT checksum verification") {
		t.Errorf("expected a loud WARN about unverified download; stderr=\n%s", buf.String())
	}
	if _, err := os.Stat(target + ".sha256"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no .sha256 should be stored when upstream has none; stat err=%v", err)
	}
}

func TestDownloadTarball_ChecksumFetchFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	fc := &routedHTTP{routes: map[string]fakeResponse{
		ChecksumURL("17.3"): {status: 500},
		TarballURL("17.3"):  {status: 200, body: "never-reached"},
	}}
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), fc, "17.3", target, &buf)
	if err == nil {
		t.Fatal("expected error when the .sha256 fetch fails with 500")
	}
	if !strings.Contains(err.Error(), "unexpected status 500") {
		t.Errorf("error %q should mention the 500", err.Error())
	}
	// Fail closed: the tarball must not have been requested at all.
	for _, c := range fc.calls {
		if c == TarballURL("17.3") {
			t.Errorf("tarball must not be fetched when the checksum fetch fails; calls=%v", fc.calls)
		}
	}
}

func TestDownloadTarball_CachedVerifiedOffline(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	const cached = "cached-bytes"
	if err := os.WriteFile(target, []byte(cached), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".sha256", []byte(sha256Hex(cached)+"  postgresql-17.3.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// httpClient that would fail if called — a cached tarball with a
	// stored checksum verifies offline.
	fc := &fakeHTTP{err: errors.New("must not call")}
	var buf bytes.Buffer
	if err := downloadTarball(context.Background(), fc, "17.3", target, &buf); err != nil {
		t.Fatalf("cached download: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != cached {
		t.Errorf("cached file overwritten: got %q want %q", string(data), cached)
	}
	if !strings.Contains(buf.String(), "sha256=verified") {
		t.Errorf("expected cache-hit log to say the hash was verified; stderr=\n%s", buf.String())
	}
}

func TestDownloadTarball_CachedHashMismatchIsFatal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	if err := os.WriteFile(target, []byte("evil-or-corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".sha256", []byte(sha256Hex("what-was-downloaded")+"  postgresql-17.3.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), &fakeHTTP{err: errors.New("must not call")}, "17.3", target, &buf)
	if err == nil {
		t.Fatal("expected error when cached tarball fails verification")
	}
	if !strings.Contains(err.Error(), "delete") || !strings.Contains(err.Error(), target) {
		t.Errorf("error %q should tell the user to delete %s and retry", err.Error(), target)
	}
	// The poisoned file must NOT be silently replaced or trusted.
	data, _ := os.ReadFile(target)
	if string(data) != "evil-or-corrupt" {
		t.Errorf("cached file was rewritten: %q", string(data))
	}
}

func TestDownloadTarball_CachedWithoutStoredSha_FetchesAndVerifies(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	const cached = "cached-bytes"
	if err := os.WriteFile(target, []byte(cached), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := &routedHTTP{routes: map[string]fakeResponse{
		ChecksumURL("17.3"): {status: 200, body: sha256Hex(cached) + "  postgresql-17.3.tar.gz\n"},
	}}
	var buf bytes.Buffer
	if err := downloadTarball(context.Background(), fc, "17.3", target, &buf); err != nil {
		t.Fatalf("cached download without stored sha: %v", err)
	}
	// Only the .sha256 may be fetched; the tarball itself is reused.
	if len(fc.calls) != 1 || fc.calls[0] != ChecksumURL("17.3") {
		t.Errorf("expected exactly one .sha256 request, got %v", fc.calls)
	}
	// The fetched digest is stored for future offline verification.
	sha, err := os.ReadFile(target + ".sha256")
	if err != nil {
		t.Fatalf("read stored .sha256: %v", err)
	}
	if !strings.Contains(string(sha), sha256Hex(cached)) {
		t.Errorf("stored .sha256 %q should contain digest %s", string(sha), sha256Hex(cached))
	}
}

func TestDownloadTarball_CachedWithoutStoredSha_Upstream404Warns(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	const cached = "cached-bytes"
	if err := os.WriteFile(target, []byte(cached), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := &routedHTTP{routes: map[string]fakeResponse{
		ChecksumURL("17.3"): {status: 404},
	}}
	var buf bytes.Buffer
	if err := downloadTarball(context.Background(), fc, "17.3", target, &buf); err != nil {
		t.Fatalf("cached download with upstream 404: %v", err)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "WITHOUT verification") {
		t.Errorf("expected a loud WARN about unverified cache reuse; stderr=\n%s", buf.String())
	}
}

func TestDownloadTarball_CachedCorruptStoredShaIsFatal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql-17.3.tar.gz")
	if err := os.WriteFile(target, []byte("cached-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".sha256", []byte("not a digest at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err := downloadTarball(context.Background(), &fakeHTTP{err: errors.New("must not call")}, "17.3", target, &buf)
	if err == nil {
		t.Fatal("expected error on corrupt stored .sha256")
	}
	if !strings.Contains(err.Error(), "delete") {
		t.Errorf("error %q should tell the user to delete and retry", err.Error())
	}
}

func TestChecksumURL(t *testing.T) {
	got := ChecksumURL("17.3")
	want := "https://ftp.postgresql.org/pub/source/v17.3/postgresql-17.3.tar.gz.sha256"
	if got != want {
		t.Fatalf("ChecksumURL = %q want %q", got, want)
	}
}

func TestParseChecksumDigest(t *testing.T) {
	valid := strings.Repeat("ab", 32)
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"sha256sum format", valid + "  postgresql-17.3.tar.gz\n", valid, false},
		{"bare digest", valid, valid, false},
		{"uppercase normalized", strings.ToUpper(valid) + "  f.tar.gz", valid, false},
		{"empty", "", "", true},
		{"whitespace only", "  \n", "", true},
		{"too short", "abc123  f.tar.gz", "", true},
		{"non-hex", strings.Repeat("zz", 32) + "  f.tar.gz", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChecksumDigest(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseChecksumDigest(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("digest: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultBuildDir(t *testing.T) {
	t.Run("prefers UserCacheDir", func(t *testing.T) {
		cache := t.TempDir()
		orig := userCacheDirFn
		userCacheDirFn = func() (string, error) { return cache, nil }
		t.Cleanup(func() { userCacheDirFn = orig })

		got, err := defaultBuildDir()
		if err != nil {
			t.Fatalf("defaultBuildDir: %v", err)
		}
		want := filepath.Join(cache, "pg_sandbox", "build")
		if got != want {
			t.Errorf("defaultBuildDir = %q want %q", got, want)
		}
	})

	t.Run("falls back to MkdirTemp when UserCacheDir fails", func(t *testing.T) {
		orig := userCacheDirFn
		userCacheDirFn = func() (string, error) { return "", errors.New("no HOME") }
		t.Cleanup(func() { userCacheDirFn = orig })

		got, err := defaultBuildDir()
		if err != nil {
			t.Fatalf("defaultBuildDir fallback: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(got) })
		st, err := os.Stat(got)
		if err != nil || !st.IsDir() {
			t.Fatalf("fallback dir %q not usable: %v", got, err)
		}
		// MkdirTemp guarantees 0o700 and an unpredictable suffix.
		if st.Mode().Perm() != 0o700 {
			t.Errorf("fallback dir perm = %04o want 0700", st.Mode().Perm())
		}
		if !strings.Contains(filepath.Base(got), "pg_sandbox-build-") {
			t.Errorf("fallback dir %q should carry the pg_sandbox-build- prefix", got)
		}
	})
}

func TestVerifyBuildDirTrust(t *testing.T) {
	t.Run("private dir passes", func(t *testing.T) {
		dir := t.TempDir() // 0o700 by construction
		if err := verifyBuildDirTrust(dir); err != nil {
			t.Errorf("private dir rejected: %v", err)
		}
	})

	t.Run("0755 dir passes (no group/world WRITE)", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := verifyBuildDirTrust(dir); err != nil {
			t.Errorf("0755 dir rejected: %v", err)
		}
	})

	t.Run("group-writable dir fails", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o770); err != nil {
			t.Fatal(err)
		}
		err := verifyBuildDirTrust(dir)
		if err == nil {
			t.Fatal("expected error for group-writable dir")
		}
		if !strings.Contains(err.Error(), "writable") {
			t.Errorf("error %q should mention writability", err.Error())
		}
	})

	t.Run("world-writable dir fails", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := verifyBuildDirTrust(dir); err == nil {
			t.Fatal("expected error for world-writable dir")
		}
	})

	t.Run("regular file fails", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyBuildDirTrust(f); err == nil {
			t.Fatal("expected error for non-directory")
		}
	})

	t.Run("missing path fails", func(t *testing.T) {
		if err := verifyBuildDirTrust(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

func TestBuild_RejectsUnsafeBuildDir(t *testing.T) {
	bin := t.TempDir()
	build := t.TempDir()
	// Simulate an attacker-friendly pre-existing dir: world-writable.
	if err := os.Chmod(build, 0o777); err != nil {
		t.Fatal(err)
	}
	swapBuildSeams(t, fakeDownload, makeExtractStep(build, "17.3", ""))

	var buf bytes.Buffer
	_, err := Build(context.Background(), Options{
		Version:  "17.3",
		BinDir:   bin,
		BuildDir: build,
	}, &buf)
	if err == nil {
		t.Fatal("expected error for world-writable build dir")
	}
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("want *BuildError, got %T", err)
	}
	if be.ExitCode.Int() != 29 {
		t.Errorf("ExitCode = %d, want 29 (ExitBuildFailed)", be.ExitCode.Int())
	}
	if !strings.Contains(err.Error(), build) {
		t.Errorf("error %q should name the offending dir %q", err.Error(), build)
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
		// MED-6: the existing dir is a live install of a different
		// version, so the non-force error must NOT steer the user toward
		// --force (that path would destroy the other install).
		if strings.Contains(err.Error(), "--force") {
			t.Errorf("mismatch error must not suggest --force; got: %v", err)
		}
	})
}

// TestBuild_VersionShapedBinDir_MismatchRefusesForce covers MED-6: when a
// version-shaped bin-dir's basename disagrees with the build version, the
// existing directory is a live install of that OTHER version. Building a
// different version into it — even with --force — must be refused with
// nothing removed, and the error must not steer the user toward --force.
// The guard fires before any download/extract, so no seams are swapped
// (as in TestBuild_RejectsExistingInstall).
func TestBuild_VersionShapedBinDir_MismatchRefusesForce(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "16.4")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sentinel standing in for the live 16.4 install that must survive.
	sentinel := filepath.Join(bin, "sentinel-live.txt")
	const live = "live 16.4 install"
	if err := os.WriteFile(sentinel, []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	_, err := Build(context.Background(), Options{
		Version:  "18.4",
		BinDir:   bin,
		BuildDir: t.TempDir(),
		Force:    true,
	}, &buf)
	if err == nil {
		t.Fatal("expected error building a mismatched version into a version-shaped bin-dir with --force")
	}
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("want *BuildError, got %T (%v)", err, err)
	}
	if be.ExitCode.Int() != 29 {
		t.Errorf("ExitCode = %d, want 29 (ExitBuildFailed)", be.ExitCode.Int())
	}
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("error must not steer the user toward --force; got: %v", err)
	}
	if !strings.Contains(err.Error(), bin) {
		t.Errorf("error should name the install prefix %q; got: %v", bin, err)
	}
	// The live install must be untouched: nothing removed.
	data, readErr := os.ReadFile(sentinel)
	if readErr != nil {
		t.Fatalf("live-install sentinel was removed with --force: %v", readErr)
	}
	if string(data) != live {
		t.Errorf("live-install sentinel was modified: got %q want %q", string(data), live)
	}
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
