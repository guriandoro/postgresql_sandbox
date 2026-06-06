// Helper for resolving a source-sandbox reference to an on-disk
// path. Shared by deploy (physical standby path) and destroy
// (best-effort slot cleanup at the source).
//
// The user supplies a "source name" on the command line — usually
// the basename of a sibling sandbox directory. We accept three
// shapes, in the order documented in SPEC §6.1's replication path:
//
//  1. An absolute path. Trusted as-is; the caller already knew
//     exactly where the source lives. Useful for cross-tree
//     sandboxes (rare, but supported).
//
//  2. A path containing a separator but not absolute. Resolved
//     relative to the cwd. Falls through to the same check as
//     absolute once made absolute, so cwd-relative refs work too.
//
//  3. A bare name (no separator). Joined onto filepath.Dir of the
//     target sandbox's path. This is the common case — primary and
//     standby live as siblings, so `--replicate-from primary`
//     resolves to `<root>/primary` for any standby being deployed
//     under `<root>/standby1`.
//
// We never silently fall through across resolution shapes. Each
// candidate path tried is recorded in the returned error message so
// users see exactly where we looked. The error wraps
// ExitSourceUnreachable so the CLI layer maps it to the right
// numeric exit code.

package sandbox

import (
	"fmt"
	"path/filepath"

	"github.com/guriandoro/postgresql_sandbox/internal/config"
)

// resolveSourceSandbox turns a user-supplied source-name reference
// into an absolute path to the source sandbox dir. targetDir is the
// destination sandbox dir being deployed (or destroyed) — its parent
// is the sibling-resolution root.
//
// On success the returned path is absolute and points at a
// directory that contains a valid pg_sandbox.json. On failure the
// returned error wraps ExitSourceUnreachable.
func resolveSourceSandbox(targetDir, name string) (string, error) {
	if name == "" {
		return "", wrapExit(ExitUsage,
			fmt.Errorf("sandbox: resolveSourceSandbox: empty source name"))
	}

	var tried []string

	// Shape 1: absolute path. Trust the user and check directly.
	if filepath.IsAbs(name) {
		tried = append(tried, name)
		if config.IsSandboxDir(name) {
			return filepath.Clean(name), nil
		}
		return "", wrapExit(ExitSourceUnreachable,
			fmt.Errorf("source sandbox %q is not a sandbox (no %s); tried: %v",
				name, config.SandboxFilename, tried))
	}

	// Shape 2: relative path containing a separator. Resolve to abs
	// from the current working directory, since the user explicitly
	// wrote a path rather than a bare name.
	if filepath.Dir(name) != "." {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", fmt.Errorf("sandbox: resolveSourceSandbox: abs(%s): %w", name, err)
		}
		tried = append(tried, abs)
		if config.IsSandboxDir(abs) {
			return filepath.Clean(abs), nil
		}
		return "", wrapExit(ExitSourceUnreachable,
			fmt.Errorf("source sandbox %q is not a sandbox (no %s); tried: %v",
				name, config.SandboxFilename, tried))
	}

	// Shape 3: bare name. Look for a sibling under the target's parent.
	// targetDir must be absolute (deploy normalizes early; destroy
	// receives the user's argv which the CLI does NOT auto-absolutize,
	// so we re-normalize here as a defensive measure).
	parent := filepath.Dir(targetDir)
	if !filepath.IsAbs(parent) {
		abs, err := filepath.Abs(parent)
		if err != nil {
			return "", fmt.Errorf("sandbox: resolveSourceSandbox: abs(parent=%s): %w", parent, err)
		}
		parent = abs
	}
	sibling := filepath.Join(parent, name)
	tried = append(tried, sibling)
	if config.IsSandboxDir(sibling) {
		return filepath.Clean(sibling), nil
	}
	return "", wrapExit(ExitSourceUnreachable,
		fmt.Errorf("source sandbox %q not found; tried: %v", name, tried))
}
