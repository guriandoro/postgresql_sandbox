// Unit tests for the argv pre-processing helpers. See argv.go.

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestReorderBoolFlags_promotesTrailingFlag(t *testing.T) {
	// Happy path: a single bool flag after a single positional must
	// be moved to the front so flag.FlagSet.Parse sees it.
	got := reorderBoolFlags(
		[]string{"18.3", "--force"},
		[]string{"force", "f"},
	)
	want := []string{"--force", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_mixedPositionalAndFlag(t *testing.T) {
	// Flag sandwiched between two positionals; both positionals must
	// keep their relative order and follow the flag.
	got := reorderBoolFlags(
		[]string{"18.3", "-f", "16.5"},
		[]string{"force", "f"},
	)
	want := []string{"-f", "18.3", "16.5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_noopWhenAlreadyOrdered(t *testing.T) {
	// When the flag is already before the positional, the result is
	// identical (no spurious reshuffling).
	got := reorderBoolFlags(
		[]string{"--force", "18.3"},
		[]string{"force", "f"},
	)
	want := []string{"--force", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_dashDashStopsProcessing(t *testing.T) {
	// `--` is the verbatim end-of-options marker. Anything after it
	// must NOT be promoted, even if it would otherwise match a known
	// bool flag.
	got := reorderBoolFlags(
		[]string{"18.3", "--", "--force"},
		[]string{"force", "f"},
	)
	want := []string{"18.3", "--", "--force"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_emptyInputs(t *testing.T) {
	// Boundary: empty argv and empty knownBoolFlags must be safe.
	if got := reorderBoolFlags(nil, []string{"force"}); len(got) != 0 {
		t.Errorf("nil argv: got %v, want empty", got)
	}
	got := reorderBoolFlags([]string{"a", "b"}, nil)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nil knownBoolFlags: got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_promotesEqualsValueForm(t *testing.T) {
	// `--force=true` after a positional must be promoted as-is; Go's
	// flag package accepts the `=value` form for bool flags.
	got := reorderBoolFlags(
		[]string{"18.3", "--force=true"},
		[]string{"force", "f"},
	)
	want := []string{"--force=true", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_promotesSingleDashLongForm(t *testing.T) {
	// Go's flag package treats `-force` and `--force` identically.
	// The single-dash long form must be promoted just like `--force`.
	got := reorderBoolFlags(
		[]string{"18.3", "-force"},
		[]string{"force", "f"},
	)
	want := []string{"-force", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_promotesShortEqualsFalse(t *testing.T) {
	// `-f=false` after a positional must be promoted as-is. The
	// original token (with dashes and `=value`) is what Parse needs
	// to see.
	got := reorderBoolFlags(
		[]string{"18.3", "-f=false"},
		[]string{"force", "f"},
	)
	want := []string{"-f=false", "18.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_doesNotPromoteDifferentName(t *testing.T) {
	// `--forces` is a different name; it must NOT be promoted just
	// because it shares a prefix with `force`.
	got := reorderBoolFlags(
		[]string{"18.3", "--forces"},
		[]string{"force", "f"},
	)
	want := []string{"18.3", "--forces"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderBoolFlags_doesNotPromoteValueTakingFlag(t *testing.T) {
	// `--root` is a value-taking flag whose bare name isn't in the
	// bool list, so it must NOT be promoted. (Promoting it would
	// separate it from its value and break parsing.)
	got := reorderBoolFlags(
		[]string{"18.3", "--root", "/tmp/sb"},
		[]string{"force", "f"},
	)
	want := []string{"18.3", "--root", "/tmp/sb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRunCleanupInstallVersions_relativeSandboxRootIsAbsoluted(t *testing.T) {
	// Pins the 2026-06-04 defense-in-depth banner contract: a
	// relative PGS_SANDBOX_ROOT (or globalCfg.SandboxRoot) must be
	// normalized to an absolute path BEFORE it reaches RenderPlan
	// and collectSandboxBinDirs. Otherwise the banner displays a
	// meaningless relative string and the walk resolves against
	// whatever CWD pg_sandbox happened to be invoked from — the
	// exact failure described in cleanup-install-versions-pitfall.md.
	//
	// We arrange a temp dir containing both the install root
	// ("bin/") and the sandbox root ("sandboxes/"), chdir into it,
	// then export both as relative paths via the env. The banner
	// must print the absolute resolved paths.
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "sandboxes"), 0o755); err != nil {
		t.Fatal(err)
	}

	// chdir into tmp so the relative "./bin" / "./sandboxes" values
	// resolve to known absolute paths. Save/restore CWD manually so
	// the test works on go.mod's 1.22 baseline (t.Chdir is 1.24+).
	origCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCWD) })

	// Force HOME away from the user's real home so the test never
	// touches it (the default sandboxRoot is filepath.Join(home,
	// "postgresql-sandboxes") — irrelevant here since we set the env
	// var, but cheap insurance).
	t.Setenv("HOME", tmp)
	t.Setenv("PGS_BIN_DIR", "./bin")
	t.Setenv("PGS_SANDBOX_ROOT", "./sandboxes")
	// Make sure no stray global config sneaks in via XDG.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	// Resolve symlinks in tmp because on macOS /var → /private/var,
	// and Go's filepath.Abs from a chdir'd CWD reflects whatever the
	// kernel reports for getwd (the resolved path). Without this,
	// the expected and observed banner paths differ by a /private
	// prefix even though they refer to the same directory.
	realTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	wantBin := filepath.Join(realTmp, "bin")
	wantSandbox := filepath.Join(realTmp, "sandboxes")

	var stdout, stderr bytes.Buffer
	// --force so the absence of a TTY doesn't matter, and there's
	// nothing under bin/ so Plan returns empty → exit 0.
	rc := runCleanupInstallVersions([]string{"--force"}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0; stderr=%q stdout=%q", rc, stderr.String(), stdout.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Install root:          "+wantBin) {
		t.Errorf("banner missing absolute install root %q; got:\n%s", wantBin, out)
	}
	if !strings.Contains(out, "Scanning sandbox root: "+wantSandbox) {
		t.Errorf("banner missing absolute sandbox root %q; got:\n%s", wantSandbox, out)
	}
	// Belt-and-suspenders: the relative literals must NOT appear in
	// the banner lines, because that's exactly the symptom of the
	// bug this test pins.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Install root:") || strings.HasPrefix(line, "Scanning sandbox root:") {
			if strings.Contains(line, "./bin") || strings.Contains(line, "./sandboxes") {
				t.Errorf("banner line still contains relative path: %q", line)
			}
		}
	}
}

func TestReorderBoolFlags_buildSubcommandFlagSet(t *testing.T) {
	// Pins the build subcommand's reorder contract. runBuild's own
	// Build() side effects (download, extract, configure, make) make
	// a direct unit test prohibitively expensive, so instead we
	// reconstruct the same FlagSet shape that build.go declares —
	// bool flags --with-icu / --with-openssl / --force / -f mixed with
	// value-taking --jobs/-j / --bin-dir/-b / --build-dir /
	// --configure-opts — and assert that reorderBoolFlags(args,
	// boolFlagNames(fs)) leaves --jobs adjacent to its value while
	// promoting only the bool flags past the positional <version>.
	//
	// The natural invocation `pg_sandbox build 17.3 --force` is the
	// regression this test guards against: without the reorder,
	// fs.Parse stops at "17.3" and --force becomes a positional → the
	// "only one version may be built at a time" error fires.
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	var (
		withICU       bool
		withOpenSSL   bool
		configureOpts string
		jobs          int
		force         bool
		binDir        string
		buildDir      string
	)
	fs.BoolVar(&withICU, "with-icu", false, "")
	fs.BoolVar(&withOpenSSL, "with-openssl", false, "")
	fs.StringVar(&configureOpts, "configure-opts", "", "")
	fs.IntVar(&jobs, "jobs", 0, "")
	fs.IntVar(&jobs, "j", 0, "")
	fs.BoolVar(&force, "force", false, "")
	fs.BoolVar(&force, "f", false, "")
	fs.StringVar(&binDir, "bin-dir", "", "")
	fs.StringVar(&binDir, "b", "", "")
	fs.StringVar(&buildDir, "build-dir", "", "")

	// Each case exercises a bool-flag-after-positional shape that
	// fs.Parse alone would mis-handle. We deliberately do NOT include
	// cases that put value-taking flags AFTER the positional: stdlib
	// `flag` stops at the first non-flag, and our reorder only
	// promotes bools (by design — promoting --jobs would separate it
	// from its value). Users who want to mix value-taking flags must
	// still put them before the positional. The helper's job is only
	// to rescue the natural `<cmd> <version> --bool` invocation.
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "version then --force",
			in:   []string{"17.3", "--force"},
			want: []string{"--force", "17.3"},
		},
		{
			name: "version then -f short alias",
			in:   []string{"17.3", "-f"},
			want: []string{"-f", "17.3"},
		},
		{
			name: "version then --with-icu --with-openssl --force",
			in:   []string{"17.3", "--with-icu", "--with-openssl", "--force"},
			want: []string{"--with-icu", "--with-openssl", "--force", "17.3"},
		},
		{
			name: "value-taking --jobs before positional, --force after",
			in:   []string{"--jobs", "4", "17.3", "--force"},
			want: []string{"--force", "--jobs", "4", "17.3"},
		},
		{
			name: "value-taking --bin-dir before positional, -f after",
			in:   []string{"--bin-dir", "/opt/pg", "17.3", "-f"},
			want: []string{"-f", "--bin-dir", "/opt/pg", "17.3"},
		},
	}

	known := boolFlagNames(fs)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderBoolFlags(tc.in, known)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reorderBoolFlags(%v) = %v, want %v", tc.in, got, tc.want)
			}
			// End-to-end: fs.Parse against the reordered argv must
			// honor --force and leave only the version as a positional.
			// Reset state between runs via a fresh FlagSet so prior
			// cases don't leak through the shared variables above.
			parseFS := flag.NewFlagSet("build", flag.ContinueOnError)
			parseFS.SetOutput(&bytes.Buffer{})
			var (
				pWithICU       bool
				pWithOpenSSL   bool
				pConfigureOpts string
				pJobs          int
				pForce         bool
				pBinDir        string
				pBuildDir      string
			)
			parseFS.BoolVar(&pWithICU, "with-icu", false, "")
			parseFS.BoolVar(&pWithOpenSSL, "with-openssl", false, "")
			parseFS.StringVar(&pConfigureOpts, "configure-opts", "", "")
			parseFS.IntVar(&pJobs, "jobs", 0, "")
			parseFS.IntVar(&pJobs, "j", 0, "")
			parseFS.BoolVar(&pForce, "force", false, "")
			parseFS.BoolVar(&pForce, "f", false, "")
			parseFS.StringVar(&pBinDir, "bin-dir", "", "")
			parseFS.StringVar(&pBinDir, "b", "", "")
			parseFS.StringVar(&pBuildDir, "build-dir", "", "")
			if err := parseFS.Parse(got); err != nil {
				t.Fatalf("Parse(%v) failed: %v", got, err)
			}
			rest := parseFS.Args()
			if len(rest) != 1 || rest[0] != "17.3" {
				t.Errorf("after Parse, positional args = %v, want [17.3]", rest)
			}
			// Every case in this table sets --force or -f, so the
			// flag should be true post-parse. This is the load-bearing
			// invariant: without reorderBoolFlags it would be false.
			if !pForce {
				t.Errorf("after Parse, force = false, want true (reorder didn't honor the bool flag)")
			}
		})
	}
}

func TestBoolFlagNames(t *testing.T) {
	// boolFlagNames must return exactly the bare names of every
	// BoolVar-registered flag on the FlagSet, and must skip flags
	// that take a value (StringVar, IntVar, etc.). Order doesn't
	// matter — reorderBoolFlags treats the slice as a set — so we
	// compare after sorting.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var (
		force   bool
		f       bool
		root    string
		binDir  string
		retries int
	)
	fs.BoolVar(&force, "force", false, "")
	fs.BoolVar(&f, "f", false, "")
	fs.StringVar(&root, "root", "", "")
	fs.StringVar(&binDir, "bin-dir", "", "")
	fs.IntVar(&retries, "retries", 0, "")

	got := boolFlagNames(fs)
	want := []string{"f", "force"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
