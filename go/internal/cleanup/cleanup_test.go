// Unit tests for cleanup. The Plan/RenderPlan/Apply trio is covered
// here with synthetic temp dirs; the real-deploy + real-rm path is
// exercised by the smoke test described in the brief.

package cleanup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
)

// fixture builds a fake bin dir + sandbox root and returns paths so
// tests can register sandboxes referencing each version.
type fixture struct {
	binDir      string
	sandboxRoot string
	t           *testing.T
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	root := filepath.Join(tmp, "sandboxes")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{binDir: bin, sandboxRoot: root, t: t}
}

// addVersion creates an empty install dir under bin.
func (f *fixture) addVersion(v string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.binDir, v, "bin"), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

// addSandbox writes a minimal valid sandbox config referencing the
// given binDir, under sandboxRoot/<name>/.
func (f *fixture) addSandbox(name, binDirRef string) string {
	f.t.Helper()
	sbDir := filepath.Join(f.sandboxRoot, name)
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	sb := &config.Sandbox{
		Name:            name,
		BinDir:          binDirRef,
		DataDir:         filepath.Join(sbDir, "data"),
		LogFile:         filepath.Join(sbDir, "server.log"),
		Host:            "127.0.0.1",
		Port:            65500,
		Superuser:       "postgres",
		DefaultDatabase: "postgres",
		Role:            config.RolePrimary,
	}
	if err := config.SaveSandbox(sbDir, sb); err != nil {
		f.t.Fatal(err)
	}
	return sbDir
}

func TestPlan_emptyBinDir(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	plan, err := Plan(Options{BinDir: f.binDir, SandboxRoot: f.sandboxRoot}, &buf)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 0 {
		t.Errorf("len(plan) = %d, want 0", len(plan))
	}
}

func TestPlan_missingBinDir(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	plan, err := Plan(Options{
		BinDir:      filepath.Join(f.binDir, "does-not-exist"),
		SandboxRoot: f.sandboxRoot,
	}, &buf)
	if err != nil {
		t.Fatalf("missing bindir should not error, got: %v", err)
	}
	if len(plan) != 0 {
		t.Errorf("len(plan) = %d, want 0", len(plan))
	}
}

func TestPlan_classifiesUsedVsUnused(t *testing.T) {
	f := newFixture(t)
	f.addVersion("16.4")
	f.addVersion("17.3")
	f.addVersion("18.2") // unused
	used164 := f.addSandbox("a", filepath.Join(f.binDir, "16.4", "bin"))
	// 17.3 is used by a sandbox that points at the version dir
	// itself (no /bin) — Plan must still classify as in-use.
	used173 := f.addSandbox("b", filepath.Join(f.binDir, "17.3"))

	var buf bytes.Buffer
	plan, err := Plan(Options{BinDir: f.binDir, SandboxRoot: f.sandboxRoot}, &buf)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 3 {
		t.Fatalf("len(plan) = %d, want 3", len(plan))
	}

	byVer := map[string]Candidate{}
	for _, c := range plan {
		byVer[c.Version] = c
	}

	if u := byVer["16.4"].UsedBy; len(u) != 1 || u[0] != used164 {
		t.Errorf("16.4 UsedBy = %v, want [%s]", u, used164)
	}
	if u := byVer["17.3"].UsedBy; len(u) != 1 || u[0] != used173 {
		t.Errorf("17.3 UsedBy = %v, want [%s]", u, used173)
	}
	if !byVer["18.2"].IsUnused() {
		t.Errorf("18.2 should be unused; UsedBy = %v", byVer["18.2"].UsedBy)
	}
}

func TestPlan_noFalsePositiveOnSubstringPrefix(t *testing.T) {
	// Regression: a sandbox at /bin/16.5/... must NOT mark /bin/16
	// as in-use. Trailing-separator check in Plan guards this.
	f := newFixture(t)
	f.addVersion("16")
	f.addVersion("16.5")
	f.addSandbox("a", filepath.Join(f.binDir, "16.5", "bin"))

	var buf bytes.Buffer
	plan, err := Plan(Options{BinDir: f.binDir, SandboxRoot: f.sandboxRoot}, &buf)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range plan {
		if c.Version == "16" && !c.IsUnused() {
			t.Errorf("16 incorrectly marked as in-use: %v", c.UsedBy)
		}
		if c.Version == "16.5" && c.IsUnused() {
			t.Errorf("16.5 should be in-use but is unused")
		}
	}
}

func TestPlan_OnlyVersions_filters(t *testing.T) {
	f := newFixture(t)
	f.addVersion("16.4")
	f.addVersion("17.3")
	f.addVersion("18.2")

	var buf bytes.Buffer
	plan, err := Plan(Options{
		BinDir:       f.binDir,
		SandboxRoot:  f.sandboxRoot,
		OnlyVersions: []string{"17.3"},
	}, &buf)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 1 || plan[0].Version != "17.3" {
		t.Errorf("plan = %+v, want only 17.3", plan)
	}
}

func TestPlan_OnlyVersions_unknownIsError(t *testing.T) {
	f := newFixture(t)
	f.addVersion("16.4")
	var buf bytes.Buffer
	_, err := Plan(Options{
		BinDir:       f.binDir,
		SandboxRoot:  f.sandboxRoot,
		OnlyVersions: []string{"16.4", "99.99"},
	}, &buf)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
	if !strings.Contains(err.Error(), "99.99") {
		t.Errorf("error %q should mention 99.99", err.Error())
	}
	if got := ExitCodeFor(err); got.Int() != 2 {
		t.Errorf("ExitCodeFor = %d, want 2 (ExitUsage)", got.Int())
	}
}

func TestApply_removesOnlyUnused(t *testing.T) {
	f := newFixture(t)
	f.addVersion("16.4")
	f.addVersion("17.3")
	f.addSandbox("a", filepath.Join(f.binDir, "16.4", "bin"))

	var buf bytes.Buffer
	plan, err := Plan(Options{BinDir: f.binDir, SandboxRoot: f.sandboxRoot}, &buf)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	removed, err := Apply(plan, &buf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(f.binDir, "16.4")); err != nil {
		t.Errorf("16.4 (in-use) was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.binDir, "17.3")); !os.IsNotExist(err) {
		t.Errorf("17.3 (unused) should be gone; stat err = %v", err)
	}
}

func TestRenderPlan_emitsScanRootHeader(t *testing.T) {
	// RenderPlan must announce the resolved scan root and the NOTE
	// block above the table. Defense-in-depth banner added after the
	// 2026-06-04 incident — see cleanup-install-versions-pitfall.md.
	f := newFixture(t)
	f.addVersion("16.4")
	f.addVersion("17.3")

	var planBuf, walkBuf bytes.Buffer
	plan, err := Plan(Options{BinDir: f.binDir, SandboxRoot: f.sandboxRoot}, &walkBuf)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	RenderPlan(&planBuf, f.binDir, f.sandboxRoot, plan)

	out := planBuf.String()
	if !strings.Contains(out, "Install root:          "+f.binDir) {
		t.Errorf("RenderPlan output missing Install-root banner naming %s; got:\n%s", f.binDir, out)
	}
	if !strings.Contains(out, "Scanning sandbox root: "+f.sandboxRoot) {
		t.Errorf("RenderPlan output missing scan-root banner; got:\n%s", out)
	}
	if !strings.Contains(out, "Only sandboxes under this root are considered") {
		t.Errorf("RenderPlan output missing NOTE block; got:\n%s", out)
	}
	if !strings.Contains(out, "PGS_SANDBOX_ROOT") {
		t.Errorf("RenderPlan output missing PGS_SANDBOX_ROOT hint; got:\n%s", out)
	}
	if !strings.Contains(out, "--root <path>") {
		t.Errorf("RenderPlan output missing --root <path> hint; got:\n%s", out)
	}
	if !strings.Contains(out, "global config's") {
		t.Errorf("RenderPlan output missing global config hint; got:\n%s", out)
	}
	if strings.Contains(out, "rebuild") {
		t.Errorf("RenderPlan output should not mention 'rebuild'; got:\n%s", out)
	}
	// The header must precede the table, and the install-root line
	// must come before the sandbox-root line.
	installIdx := strings.Index(out, "Install root:")
	bannerIdx := strings.Index(out, "Scanning sandbox root:")
	tableIdx := strings.Index(out, "VERSION")
	if installIdx < 0 || bannerIdx < 0 || tableIdx < 0 || installIdx >= bannerIdx || bannerIdx >= tableIdx {
		t.Errorf("header order wrong; install=%d banner=%d table=%d", installIdx, bannerIdx, tableIdx)
	}
}

func TestRenderPlan_emitsHeaderOnEmptyPlan(t *testing.T) {
	// The header is the whole point of the change; it must appear
	// even when there are zero candidates (e.g. no installs yet, or
	// every candidate filtered out). On a no-op run the user should
	// still see what was scanned — both the install root (so a
	// first-run user with an empty PGS_BIN_DIR can see which knob to
	// reach for) and the sandbox root.
	var buf bytes.Buffer
	RenderPlan(&buf, "/some/bin/dir", "/some/scan/root", nil)
	out := buf.String()
	if !strings.Contains(out, "Install root:          /some/bin/dir") {
		t.Errorf("empty-plan output missing Install-root banner; got:\n%s", out)
	}
	if !strings.Contains(out, "Scanning sandbox root: /some/scan/root") {
		t.Errorf("empty-plan output missing scan-root banner; got:\n%s", out)
	}
	if !strings.Contains(out, "Only sandboxes under this root are considered") {
		t.Errorf("empty-plan output missing NOTE block; got:\n%s", out)
	}
	if !strings.Contains(out, "no install versions found under /some/bin/dir") {
		t.Errorf("empty-plan output missing 'no install versions found under <binDir>' line; got:\n%s", out)
	}
}

func TestConfirm_yes(t *testing.T) {
	var buf bytes.Buffer
	if !Confirm(strings.NewReader("y\n"), &buf, 3) {
		t.Error("y should be yes")
	}
	if !Confirm(strings.NewReader("YES\n"), &buf, 3) {
		t.Error("YES should be yes")
	}
}

func TestConfirm_no(t *testing.T) {
	var buf bytes.Buffer
	for _, in := range []string{"", "n", "no", "huh", "Y "} {
		// "Y " with trailing space → trimmed to "Y" → lowered to "y" → yes.
		// But our point here is bare "y"/"yes" only.
		want := false
		if strings.TrimSpace(strings.ToLower(in)) == "y" || strings.TrimSpace(strings.ToLower(in)) == "yes" {
			want = true
		}
		got := Confirm(strings.NewReader(in+"\n"), &buf, 1)
		if got != want {
			t.Errorf("Confirm(%q) = %v, want %v", in, got, want)
		}
	}
}
