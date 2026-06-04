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
	if len(plan.Candidates) != 0 {
		t.Errorf("len(plan.Candidates) = %d, want 0", len(plan.Candidates))
	}
	if plan.BinDirMissing {
		t.Errorf("BinDirMissing = true for an existing-but-empty bin dir; should be false")
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
	if len(plan.Candidates) != 0 {
		t.Errorf("len(plan.Candidates) = %d, want 0", len(plan.Candidates))
	}
	if !plan.BinDirMissing {
		t.Errorf("BinDirMissing = false for a missing bin dir; should be true")
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
	if len(plan.Candidates) != 3 {
		t.Fatalf("len(plan.Candidates) = %d, want 3", len(plan.Candidates))
	}

	byVer := map[string]Candidate{}
	for _, c := range plan.Candidates {
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
	for _, c := range plan.Candidates {
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
	if len(plan.Candidates) != 1 || plan.Candidates[0].Version != "17.3" {
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

	removed, err := Apply(plan.Candidates, &buf)
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
	if !strings.Contains(out, "Only sandboxes under the sandbox root are considered") {
		t.Errorf("RenderPlan output missing NOTE block; got:\n%s", out)
	}
	if !strings.Contains(out, "PGS_SANDBOX_ROOT") {
		t.Errorf("RenderPlan output missing PGS_SANDBOX_ROOT hint; got:\n%s", out)
	}
	if !strings.Contains(out, "--root <path>") {
		t.Errorf("RenderPlan output missing --root <path> hint; got:\n%s", out)
	}
	if !strings.Contains(out, "sandboxRoot") {
		t.Errorf("RenderPlan output missing sandboxRoot global-config hint; got:\n%s", out)
	}
	// Install-root knobs must be named symmetrically with the
	// sandbox-root knobs — a first-run user whose --bin-dir / PGS_BIN_DIR
	// is empty needs to see which knob to reach for.
	if !strings.Contains(out, "--bin-dir <path>") {
		t.Errorf("RenderPlan output missing --bin-dir <path> hint; got:\n%s", out)
	}
	if !strings.Contains(out, "PGS_BIN_DIR") {
		t.Errorf("RenderPlan output missing PGS_BIN_DIR hint; got:\n%s", out)
	}
	if !strings.Contains(out, "defaultBinDir") {
		t.Errorf("RenderPlan output missing defaultBinDir global-config hint; got:\n%s", out)
	}
	// Anchor on the full deleted phrase rather than the bare substring
	// "rebuild": f.binDir comes from t.TempDir(), and a custom TMPDIR
	// containing "rebuild" (e.g. /tmp/rebuild-cache/...) would otherwise
	// trip this guard spuriously. The original wording we want to
	// stay-deleted was: "Set PGS_SANDBOX_ROOT or rebuild with a
	// different root if you need a wider scan."
	if strings.Contains(out, "rebuild with a different root") {
		t.Errorf("RenderPlan output should not mention the pre-PR 'rebuild with a different root' phrase; got:\n%s", out)
	}
	// The header must precede the table, and the install-root line
	// must come before the sandbox-root line.
	//
	// The order check is line-anchored (find the line that STARTS
	// WITH each marker) rather than using strings.Index over the whole
	// buffer. Why: the NOTE prose already mentions knobs like
	// "--bin-dir <path>" and "PGS_BIN_DIR", and a future maintainer
	// might add prose like "(Install root)" or rephrase to mention
	// "Scanning sandbox root" inline. A byte-index search would then
	// silently measure NOTE-prose positions instead of the actual
	// label lines, and the invariant could rot while the test still
	// passes. Anchoring on line prefixes makes the check robust to
	// prose drift anywhere else in the output.
	installLine := lineIndexWithPrefix(out, "Install root:")
	bannerLine := lineIndexWithPrefix(out, "Scanning sandbox root:")
	tableLine := lineIndexWithPrefix(out, "VERSION")
	if installLine < 0 || bannerLine < 0 || tableLine < 0 || installLine >= bannerLine || bannerLine >= tableLine {
		t.Errorf("header order wrong; install=%d banner=%d table=%d\nout:\n%s", installLine, bannerLine, tableLine, out)
	}
}

// lineIndexWithPrefix returns the 0-based index of the first line in
// s whose content starts with prefix, or -1 if no such line exists.
// Used by the header-order check so prose mentions of label
// substrings elsewhere in the output can't fool the assertion.
func lineIndexWithPrefix(s, prefix string) int {
	for i, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	return -1
}

func TestRenderPlan_emitsHeaderOnEmptyPlan(t *testing.T) {
	// The header is the whole point of the change; it must appear
	// even when there are zero candidates (e.g. no installs yet, or
	// every candidate filtered out). On a no-op run the user should
	// still see what was scanned — both the install root (so a
	// first-run user with an empty PGS_BIN_DIR can see which knob to
	// reach for) and the sandbox root.
	var buf bytes.Buffer
	RenderPlan(&buf, "/some/bin/dir", "/some/scan/root", PlanResult{})
	out := buf.String()
	if !strings.Contains(out, "Install root:          /some/bin/dir") {
		t.Errorf("empty-plan output missing Install-root banner; got:\n%s", out)
	}
	if !strings.Contains(out, "Scanning sandbox root: /some/scan/root") {
		t.Errorf("empty-plan output missing scan-root banner; got:\n%s", out)
	}
	if !strings.Contains(out, "Only sandboxes under the sandbox root are considered") {
		t.Errorf("empty-plan output missing NOTE block; got:\n%s", out)
	}
	if !strings.Contains(out, "no install versions found under /some/bin/dir") {
		t.Errorf("empty-plan output missing 'no install versions found under <binDir>' line; got:\n%s", out)
	}
	if strings.Contains(out, "does not exist") {
		t.Errorf("empty-but-existing bin dir should NOT say 'does not exist'; got:\n%s", out)
	}
}

func TestRenderPlan_missingBinDirSaysSo(t *testing.T) {
	// Regression: pre-fix, a missing --bin-dir collapsed to the
	// same "no install versions found under <dir>" line as an
	// existing-but-empty install root. That read like a successful
	// no-op scan and masked a typo'd or never-created path. With
	// PlanResult.BinDirMissing threaded through, RenderPlan must say
	// the directory doesn't exist instead of claiming a scan
	// happened.
	var buf bytes.Buffer
	RenderPlan(&buf, "/some/missing/dir", "/some/scan/root", PlanResult{BinDirMissing: true})
	out := buf.String()
	if !strings.Contains(out, "Install root:          /some/missing/dir") {
		t.Errorf("missing-bin-dir output missing Install-root banner; got:\n%s", out)
	}
	if !strings.Contains(out, "install root /some/missing/dir does not exist") {
		t.Errorf("missing-bin-dir output should say 'does not exist'; got:\n%s", out)
	}
	if strings.Contains(out, "no install versions found under") {
		t.Errorf("missing-bin-dir output must NOT say 'no install versions found under ...' (that claims a scan happened); got:\n%s", out)
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
