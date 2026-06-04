// cleanup-install-versions implementation. See doc.go.
//
// The Plan + Apply split below makes the testability story easy:
// Plan is pure (given a binDir + sandbox root it returns "candidate ->
// in-use sandboxes"); Apply is the side-effectful step that prompts
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
	"sort"
	"strings"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
	"github.com/guriandoro/postgresql_sandbox/go/internal/ui"
)

// Options captures the CLI input to CleanupInstallVersions. Resolved
// upstream by the dispatcher.
type Options struct {
	// BinDir is the install root (each subdir is a candidate version).
	// REQUIRED. Resolves from --bin-dir / PGS_BIN_DIR in the CLI layer.
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

// Plan inventories BinDir, walks SandboxRoot, and returns one
// Candidate per version directory under BinDir. The result is sorted
// by Version (lexicographic) for stable output.
//
// stderrW is used for warn-level diagnostics during the walk (e.g.
// a malformed sandbox config in some subdirectory). The walk
// continues past those — we don't want one broken sandbox to prevent
// the user from pruning unused versions.
func Plan(opts Options, stderrW io.Writer) ([]Candidate, error) {
	if opts.BinDir == "" {
		return nil, fmt.Errorf("cleanup: BinDir is required")
	}
	if opts.SandboxRoot == "" {
		return nil, fmt.Errorf("cleanup: SandboxRoot is required")
	}
	binDir := filepath.Clean(opts.BinDir)
	root := filepath.Clean(opts.SandboxRoot)

	// 1. Inventory candidate version dirs.
	candidates := []Candidate{}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No bin dir at all → no candidates. Surface as empty
			// (not an error) so first-run UX is gentle.
			return candidates, nil
		}
		return nil, fmt.Errorf("cleanup: read %s: %w", binDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
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
		// Trailing separator so "/opt/pg/16" doesn't match a sandbox
		// referencing "/opt/pg/16.5/bin" (sub-string trap). Comparing
		// against the version-dir prefix + os.PathSeparator is the
		// cleanest "the sandbox's binDir lives UNDER this version dir"
		// check.
		prefix := c.Path + string(os.PathSeparator)
		var used []string
		for sbDir, binDirRef := range refs {
			cleanedRef := filepath.Clean(binDirRef)
			if cleanedRef == c.Path || strings.HasPrefix(cleanedRef, prefix) {
				used = append(used, sbDir)
			}
		}
		sort.Strings(used)
		candidates[i].UsedBy = used
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
			return nil, fmt.Errorf("cleanup: version(s) not found under %s: %s",
				binDir, strings.Join(missing, ", "))
		}
		candidates = filtered
	}

	return candidates, nil
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

// RenderPlan writes a human-readable summary of the candidates to w.
// Used by the CLI layer for both the "what would happen" preview and
// the "what did happen" log.
//
// sandboxRoot is the resolved scan root (post flag → env → global →
// default chain). It is announced in a header BEFORE the table so the
// user sees the scope of the cross-reference even on a no-op run.
// This is a defense-in-depth measure following the 2026-06-04
// incident where a smoke test deployed a sandbox at /tmp while the
// default sandbox root was scanned — the cross-reference missed it
// and an in-use install was pruned. See the project memory
// `cleanup-install-versions-pitfall.md`.
func RenderPlan(w io.Writer, sandboxRoot string, plan []Candidate) {
	// Always emit the scan-root banner first, regardless of whether
	// the plan has any candidates. The point is to make the scope
	// visible even on the "no unused install versions" path.
	renderScanRootHeader(w, sandboxRoot)

	if len(plan) == 0 {
		fmt.Fprintln(w, "no install versions found")
		return
	}
	colVer, colState := 7, 6
	for _, c := range plan {
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
	for _, c := range plan {
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

// renderScanRootHeader writes the "Scanning sandbox root: ..." banner
// plus the NOTE block. Plain text, no color/ANSI; the goal is to be
// visible in piped output and CI logs as well as at an interactive
// terminal.
//
// Kept exported-from-package only via RenderPlan rather than as its
// own public symbol to keep the API surface narrow — callers should
// always render the header and the table together.
func renderScanRootHeader(w io.Writer, sandboxRoot string) {
	fmt.Fprintf(w, "Scanning sandbox root: %s\n", sandboxRoot)
	fmt.Fprintln(w, "NOTE: Only sandboxes under this root are considered. Sandboxes elsewhere")
	fmt.Fprintln(w, "will NOT block removal. Set PGS_SANDBOX_ROOT or rebuild with a different")
	fmt.Fprintln(w, "root if you need a wider scan.")
	fmt.Fprintln(w)
}

// Apply removes every Candidate whose UsedBy is empty. The CLI layer
// is responsible for prompting (or skipping the prompt with --force)
// BEFORE calling Apply; Apply itself does no prompting.
//
// stderrW receives one "removed" line per version. Returns the
// number removed and the first error encountered (we continue past
// errors so a permission denial on one dir doesn't strand the
// others).
func Apply(plan []Candidate, stderrW io.Writer) (int, error) {
	logger := slog.New(slog.NewTextHandler(stderrW, nil))
	var firstErr error
	removed := 0
	for _, c := range plan {
		if !c.IsUnused() {
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
