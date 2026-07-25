// cleanup-install-versions implementation. See doc.go.
//
// The Plan + Apply split below makes the testability story easy:
// Plan is pure (given a binDir + sandbox root it returns "candidate ->
// in-use sandboxes"); Apply is the side-effectful step that re-checks
// and removes. Tests exercise Plan directly with temp dirs; Apply is
// covered by a smoke test that actually deploys + destroys a sandbox.

package cleanup

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
	"github.com/guriandoro/postgresql_sandbox/internal/ui"
)

// installVersionRE is the shape a subdirectory name must have to be
// treated as a removable install version: major.minor, decimal
// integers only. It mirrors internal/build's versionRE — the shape
// `build` accepts and installs under — so the pruner's candidate set
// is exactly the set of dirs the tool itself creates. Anything else
// under the install root (docs/, a stray tarball dir, and —
// critically — the bin/include/lib/share layout of an install prefix
// mistakenly used as the root) is never a deletion candidate.
var installVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// Options captures the CLI input to CleanupInstallVersions. Resolved
// upstream by the dispatcher.
type Options struct {
	// BinDir is the install root (each version-shaped subdir, e.g.
	// "16.4", is a candidate version). REQUIRED. Resolves from
	// --bin-dir / PGS_BIN_DIR in the CLI layer.
	BinDir string

	// SandboxRoot is the directory to walk for sandboxes-in-use.
	// REQUIRED. Resolves from --root / PGS_SANDBOX_ROOT / global config /
	// ~/postgresql-sandboxes/ in the CLI layer.
	SandboxRoot string

	// OnlyVersions, when non-empty, restricts the prune to the named
	// versions. Versions not present under BinDir are rejected by
	// CleanupInstallVersions with ExitUsage so a typo doesn't silently
	// no-op.
	OnlyVersions []string

	// Force skips the y/N confirmation prompt. Required when stdin
	// is not a TTY (the CLI layer enforces ExitNotATTY otherwise).
	Force bool
}

// Candidate is one inventory entry — a version dir under BinDir, plus
// the list of sandboxes (by dir) that reference it. Empty UsedBy means
// the version is removable.
type Candidate struct {
	// Version is the directory basename, e.g. "16.4".
	Version string

	// Path is the absolute path of the version dir under BinDir.
	Path string

	// UsedBy is the list of sandbox directories whose binDir starts
	// with Path. Empty when the version is unused. Stable order
	// (lexicographic by sandbox dir) for deterministic output.
	UsedBy []string
}

// IsUnused is a convenience predicate used by Plan callers / tests.
func (c Candidate) IsUnused() bool { return len(c.UsedBy) == 0 }

// PlanResult is the structured return of Plan. It distinguishes a
// genuinely empty install root ("scan happened, found nothing") from
// a missing install root ("nothing was scanned") so RenderPlan can
// emit different copy for each case. Pre-2026-06-04 the two collapsed
// to the same "no install versions found under %s" line, which on a
// typo'd or never-created --bin-dir read as "I scanned it, nothing to
// clean" — masking a misconfiguration.
type PlanResult struct {
	// Candidates is one entry per version directory found under
	// BinDir (or the filtered subset when OnlyVersions is set).
	// Sorted by Version (lexicographic) for stable output.
	Candidates []Candidate

	// BinDirMissing is true when the resolved BinDir does not exist
	// on disk. Candidates will be empty in that case. Set so callers
	// can distinguish "scanned and found nothing" from "no scan
	// happened because the directory isn't there".
	BinDirMissing bool
}

// Plan inventories BinDir, walks SandboxRoot, and returns one
// Candidate per version directory under BinDir. The result is sorted
// by Version (lexicographic) for stable output.
//
// A missing BinDir is NOT an error — Plan returns an empty result
// with BinDirMissing=true so the CLI can render a distinct message
// (see PlanResult).
//
// stderrW is used for warn-level diagnostics during the walk (e.g.
// a malformed sandbox config in some subdirectory). The walk
// continues past those — we don't want one broken sandbox to prevent
// the user from pruning unused versions.
func Plan(opts Options, stderrW io.Writer) (PlanResult, error) {
	if opts.BinDir == "" {
		return PlanResult{}, fmt.Errorf("cleanup: BinDir is required")
	}
	if opts.SandboxRoot == "" {
		return PlanResult{}, fmt.Errorf("cleanup: SandboxRoot is required")
	}
	binDir := filepath.Clean(opts.BinDir)
	root := filepath.Clean(opts.SandboxRoot)

	// 1. Inventory candidate version dirs.
	candidates := []Candidate{}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No bin dir at all → no candidates. Surface as empty
			// (not an error) so first-run UX is gentle, but mark
			// BinDirMissing so RenderPlan can say "does not exist"
			// rather than "scanned, found nothing".
			return PlanResult{Candidates: candidates, BinDirMissing: true}, nil
		}
		return PlanResult{}, fmt.Errorf("cleanup: read %s: %w", binDir, err)
	}

	// Refuse to prune when binDir itself looks like a single install
	// PREFIX rather than an install ROOT. The deploy/build layers
	// accept a version-shaped PGS_BIN_DIR like /opt/postgresql/16.4
	// whose children are bin/include/lib/share; treating those as
	// removable "versions" would destroy the live install (they can
	// never all be matched by a sandbox's binDir reference). The
	// version-shape filter below already excludes them, but a pointed
	// error beats a silent no-op scan of the wrong directory.
	hasBin, hasLib := false, false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		switch e.Name() {
		case "bin":
			hasBin = true
		case "lib":
			hasLib = true
		}
	}
	if hasBin && hasLib {
		return PlanResult{}, fmt.Errorf("cleanup: %s looks like a single PostgreSQL install prefix (contains bin/ and lib/), not an install root; refusing to prune — point --bin-dir / PGS_BIN_DIR at the parent directory that holds one subdirectory per version", binDir)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only version-shaped names ("16.4") are candidates. This
		// also skips hidden dirs and anything else living under the
		// install root that the tool didn't install (see
		// installVersionRE's doc).
		if !installVersionRE.MatchString(e.Name()) {
			continue
		}
		candidates = append(candidates, Candidate{
			Version: e.Name(),
			Path:    filepath.Join(binDir, e.Name()),
		})
	}

	// 2. Walk sandbox root and collect binDir references.
	refs := collectSandboxBinDirs(root, stderrW)

	// 3. Cross-reference.
	for i, c := range candidates {
		candidates[i].UsedBy = usedBy(c, refs)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Version < candidates[j].Version
	})

	// 4. Filter by OnlyVersions if supplied.
	if len(opts.OnlyVersions) > 0 {
		want := map[string]bool{}
		for _, v := range opts.OnlyVersions {
			want[v] = true
		}
		var missing []string
		filtered := make([]Candidate, 0, len(opts.OnlyVersions))
		seen := map[string]bool{}
		for _, c := range candidates {
			if want[c.Version] {
				filtered = append(filtered, c)
				seen[c.Version] = true
			}
		}
		for _, v := range opts.OnlyVersions {
			if !seen[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			return PlanResult{}, fmt.Errorf("cleanup: version(s) not found under %s: %s",
				binDir, strings.Join(missing, ", "))
		}
		candidates = filtered
	}

	return PlanResult{Candidates: candidates}, nil
}

// collectSandboxBinDirs walks root depth-bounded and returns a
// map[sandboxDir]binDir. The walk is intentionally lenient — any
// directory we can't read or any config we can't parse is logged at
// warn level and skipped. The cleanup command should not refuse to
// proceed because one sandbox is broken.
//
// We mirror the sandbox.global_status walker's design (depth-bounded,
// stop on sandbox/cluster boundary) but implement it inline rather
// than re-using sandbox.GlobalStatusWalk because GlobalStatusWalk
// emits a much richer structure than we need here. A flat
// map[dir]binDir is the right abstraction for the cross-reference.
func collectSandboxBinDirs(root string, stderrW io.Writer) map[string]string {
	const maxDepth = 4 // root → cluster → member-dir → (data/)? leaves wiggle room
	out := map[string]string{}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		// Missing sandbox root is fine (treated as "no sandboxes").
		return out
	}
	logger := slog.New(slog.NewTextHandler(stderrW, nil))

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			logger.Warn("cleanup: cannot read dir; skipping", "dir", dir, "err", err)
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if config.IsSandboxDir(sub) {
				cfg, err := config.LoadSandbox(sub)
				if err != nil {
					logger.Warn("cleanup: cannot load sandbox; skipping", "dir", sub, "err", err)
					continue
				}
				out[sub] = cfg.BinDir
				// Don't recurse into the sandbox dir; the data dir
				// below isn't useful to the cross-reference.
				continue
			}
			walk(sub, depth+1)
		}
	}
	walk(root, 0)
	return out
}

// usedBy returns the sorted list of sandbox dirs (keys of refs) whose
// binDir reference lives at or under candidate c's path. Shared by
// Plan (scan-time classification) and Apply (fresh re-check after the
// confirmation prompt).
func usedBy(c Candidate, refs map[string]string) []string {
	var used []string
	for sbDir, binDirRef := range refs {
		if refUnderCandidate(binDirRef, c.Path) {
			used = append(used, sbDir)
		}
	}
	sort.Strings(used)
	return used
}

// refUnderCandidate reports whether a sandbox's binDir reference is
// the candidate version dir itself or lives underneath it.
//
// Both sides are compared in every form pathForms yields (cleaned and
// symlink-resolved), cross-product, so a reference that reaches the
// install through a symlink still counts as in-use: `latest -> 16.4`
// (config stores /opt/pg/latest/bin) matches candidate /opt/pg/16.4,
// and /tmp vs /private/tmp on macOS match each other. Over-matching
// is the safe direction here — a false "in use" keeps a dir, a false
// "unused" deletes a live install.
//
// The trailing separator on the prefix is load-bearing: "/opt/pg/16.5"
// must not match a sandbox referencing "/opt/pg/16.50/bin" (sub-string
// trap). Comparing against candidate + os.PathSeparator is the
// cleanest "the sandbox's binDir lives UNDER this version dir" check.
func refUnderCandidate(binDirRef, candidatePath string) bool {
	refForms := pathForms(binDirRef)
	for _, cand := range pathForms(candidatePath) {
		prefix := cand + string(os.PathSeparator)
		for _, ref := range refForms {
			if ref == cand || strings.HasPrefix(ref, prefix) {
				return true
			}
		}
	}
	return false
}

// pathForms returns the comparable forms of p: always the cleaned
// path, plus the symlink-resolved path when EvalSymlinks succeeds and
// differs. Best-effort by design — a dangling reference (sandbox
// pointing at an already-deleted install) can't be resolved, and
// falling back to the cleaned form keeps it participating in the
// comparison instead of silently dropping out.
func pathForms(p string) []string {
	cleaned := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && resolved != cleaned {
		return []string{cleaned, resolved}
	}
	return []string{cleaned}
}

// RenderPlan writes a human-readable summary of the candidates to w.
// Used by the CLI layer for both the "what would happen" preview and
// the "what did happen" log.
//
// binDir is the resolved install root (where candidate version dirs
// live) and sandboxRoot is the resolved scan root (post flag → env →
// global → default chain). Both are announced in a header BEFORE the
// table so the user sees the scope of the cross-reference even on a
// no-op run — and so a first-run user whose PGS_BIN_DIR is empty or
// missing can tell that the install root, not the sandbox root, is
// the knob to tune. This is a defense-in-depth measure following the
// 2026-06-04 incident where a smoke test deployed a sandbox at /tmp
// while the default sandbox root was scanned — the cross-reference
// missed it and an in-use install was pruned. See the project memory
// `cleanup-install-versions-pitfall.md`.
func RenderPlan(w io.Writer, binDir, sandboxRoot string, plan PlanResult) {
	// Always emit the scan-root banner first, regardless of whether
	// the plan has any candidates. The point is to make the scope
	// visible even on the "no unused install versions" path.
	renderScanRootHeader(w, binDir, sandboxRoot)

	// Distinguish "directory absent" from "directory present but
	// empty". Both leave Candidates empty, but conflating them was a
	// real UX trap: a typo'd --bin-dir would print "no install
	// versions found under <typo>" and read as a successful no-op
	// scan, never tipping the user off that they pointed at a
	// non-existent path.
	if plan.BinDirMissing {
		fmt.Fprintf(w, "install root %s does not exist (nothing scanned)\n", binDir)
		return
	}
	if len(plan.Candidates) == 0 {
		fmt.Fprintf(w, "no install versions found under %s\n", binDir)
		return
	}
	colVer, colState := 7, 6
	for _, c := range plan.Candidates {
		if n := len(c.Version); n > colVer {
			colVer = n
		}
		s := "unused"
		if !c.IsUnused() {
			s = fmt.Sprintf("in use by %d", len(c.UsedBy))
		}
		if n := len(s); n > colState {
			colState = n
		}
	}
	fmt.Fprintf(w, "%-*s  %-*s  %s\n", colVer, "VERSION", colState, "STATE", "PATH")
	for _, c := range plan.Candidates {
		state := "unused"
		if !c.IsUnused() {
			state = fmt.Sprintf("in use by %d", len(c.UsedBy))
		}
		fmt.Fprintf(w, "%-*s  %-*s  %s\n", colVer, c.Version, colState, state, c.Path)
		for _, sb := range c.UsedBy {
			fmt.Fprintf(w, "  - %s\n", sb)
		}
	}
}

// renderScanRootHeader writes the two-line scope header ("Install
// root: ..." then "Scanning sandbox root: ...") plus the NOTE block.
// Plain text, no color/ANSI; the goal is to be visible in piped
// output and CI logs as well as at an interactive terminal.
//
// The install root is listed first because that's where the
// candidates come from — if it's empty/missing the plan will be
// empty and the user needs to know which knob (PGS_BIN_DIR /
// --bin-dir) to reach for. The field labels are space-padded so the
// two paths line up vertically.
//
// Kept exported-from-package only via RenderPlan rather than as its
// own public symbol to keep the API surface narrow — callers should
// always render the header and the table together.
func renderScanRootHeader(w io.Writer, binDir, sandboxRoot string) {
	fmt.Fprintf(w, "Install root:          %s\n", binDir)
	fmt.Fprintf(w, "Scanning sandbox root: %s\n", sandboxRoot)
	fmt.Fprintln(w, "NOTE: Only sandboxes under the sandbox root are considered. Sandboxes")
	fmt.Fprintln(w, "elsewhere will NOT block removal. To change the install root, pass")
	fmt.Fprintln(w, "--bin-dir <path> for a one-shot, set PGS_BIN_DIR in the environment, or")
	fmt.Fprintln(w, "update the global config's defaultBinDir. To widen the sandbox-root scan,")
	fmt.Fprintln(w, "pass --root <path> for a one-shot, set PGS_SANDBOX_ROOT in the")
	fmt.Fprintln(w, "environment, or update the global config's sandboxRoot.")
	fmt.Fprintln(w)
}

// Apply removes every Candidate whose UsedBy is empty. The CLI layer
// is responsible for prompting (or skipping the prompt with --force)
// BEFORE calling Apply; Apply itself does no prompting.
//
// Because the plan was computed before the confirmation prompt — which
// can sit open for an arbitrarily long time — Apply re-walks
// opts.SandboxRoot and re-checks every scan-time-unused candidate
// against the fresh references immediately before removing it. A
// sandbox deployed while the prompt was open marks its version in-use
// again; Apply skips it with a warn line instead of deleting it
// (TOCTOU guard, MED-5).
//
// stderrW receives one "removed" line per version. Returns the
// number removed and the first error encountered (we continue past
// errors so a permission denial on one dir doesn't strand the
// others).
func Apply(opts Options, plan []Candidate, stderrW io.Writer) (int, error) {
	if opts.SandboxRoot == "" {
		return 0, fmt.Errorf("cleanup: SandboxRoot is required")
	}
	logger := slog.New(slog.NewTextHandler(stderrW, nil))

	// Fresh sandbox walk: the scan-time UsedBy answers "was it unused
	// when we looked?"; deletion needs "is it unused NOW?".
	refs := collectSandboxBinDirs(filepath.Clean(opts.SandboxRoot), stderrW)

	var firstErr error
	removed := 0
	for _, c := range plan {
		if !c.IsUnused() {
			continue
		}
		if used := usedBy(c, refs); len(used) > 0 {
			logger.Warn("cleanup: skipping version now in use (sandbox deployed since scan)",
				"version", c.Version, "path", c.Path, "used_by", strings.Join(used, ", "))
			continue
		}
		if err := os.RemoveAll(c.Path); err != nil {
			logger.Error("cleanup: rm failed", "version", c.Version, "path", c.Path, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("cleanup: rm %s: %w", c.Path, err)
			}
			continue
		}
		removed++
		logger.Info("removed install", "version", c.Version, "path", c.Path)
	}
	return removed, firstErr
}

// Confirm prompts on stderrW and reads a single yes/no answer from
// stdinR. Returns true iff the user typed a yes-equivalent. Empty
// answer / EOF / anything-not-yes defaults to NO (defensive default
// for a destructive operation).
//
// The CLI layer calls this only after checking that stdin is a TTY;
// non-TTY callers must use Force.
func Confirm(stdinR io.Reader, stderrW io.Writer, n int) bool {
	fmt.Fprintf(stderrW, "Remove %d unused install version(s)? [y/N]: ", n)
	scanner := bufio.NewScanner(stdinR)
	if !scanner.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return ans == "y" || ans == "yes"
}

// ExitCodeFor maps a Plan error to the correct ExitCode. Unknown
// version names are usage errors; everything else is ExitGeneric.
func ExitCodeFor(err error) ui.ExitCode {
	if err == nil {
		return ui.ExitOK
	}
	msg := err.Error()
	if strings.Contains(msg, "not found under") {
		return ui.ExitUsage
	}
	return ui.ExitGeneric
}
