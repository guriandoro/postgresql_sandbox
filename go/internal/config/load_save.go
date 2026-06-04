// Atomic load/save for sandbox + global config files.
//
// Design points worth calling out:
//
//   - Strict decode. Unknown JSON keys are a hard error per SPEC
//     §3.1.4. We achieve this with json.Decoder.DisallowUnknownFields.
//
//   - Schema version gate. We refuse files with SchemaVersion >
//     CurrentSchemaVersion so a binary that doesn't know about
//     newer keys can't silently drop them on round-trip.
//
//   - Atomic writes. We write to a sibling temp file, fsync, and
//     rename over the target. The rename is atomic on every POSIX
//     filesystem we care about (APFS, ext4, xfs), so a reader can
//     never observe a half-written config — they see either the
//     old contents or the new, never a torn mix.

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrSchemaVersionTooNew indicates the on-disk file was written by
// a newer pg_sandbox than the one reading it. Wrapped, not equal-
// compared, so callers should errors.Is to detect.
var ErrSchemaVersionTooNew = errors.New("config: schemaVersion newer than supported")

// LoadSandbox reads <sandboxDir>/SandboxFilename and returns the
// parsed Sandbox. Unknown keys are an error; schemaVersion >
// CurrentSchemaVersion is an error wrapped around
// ErrSchemaVersionTooNew.
func LoadSandbox(sandboxDir string) (*Sandbox, error) {
	path := filepath.Join(sandboxDir, SandboxFilename)
	var s Sandbox
	if err := loadJSONStrict(path, &s); err != nil {
		return nil, err
	}
	if s.SchemaVersion > CurrentSchemaVersion {
		return nil, fmt.Errorf("%s: schemaVersion %d > supported %d (upgrade pg_sandbox): %w",
			path, s.SchemaVersion, CurrentSchemaVersion, ErrSchemaVersionTooNew)
	}
	return &s, nil
}

// LoadCluster reads <clusterDir>/ClusterFilename and returns the
// parsed ClusterManifest. Unknown keys are an error; schemaVersion >
// CurrentSchemaVersion is an error wrapped around
// ErrSchemaVersionTooNew. Mirrors LoadSandbox's contract exactly —
// the manifest is the same shape of strict-JSON file SPEC §3 designed
// for sandboxes, just with cluster-level keys.
func LoadCluster(clusterDir string) (*ClusterManifest, error) {
	path := filepath.Join(clusterDir, ClusterFilename)
	var m ClusterManifest
	if err := loadJSONStrict(path, &m); err != nil {
		return nil, err
	}
	if m.SchemaVersion > CurrentSchemaVersion {
		return nil, fmt.Errorf("%s: schemaVersion %d > supported %d (upgrade pg_sandbox): %w",
			path, m.SchemaVersion, CurrentSchemaVersion, ErrSchemaVersionTooNew)
	}
	return &m, nil
}

// SaveCluster atomically writes m to <clusterDir>/ClusterFilename.
// The directory must already exist — Save does NOT create it, matching
// SaveSandbox's contract (the cluster command creates the dir before
// the first save, so this is fine).
//
// On every save, LastModifiedAt is set to now. CreatedAt is preserved
// if non-zero, otherwise set to now (first save). SchemaVersion is set
// to CurrentSchemaVersion when zero so partial callers can synthesize
// a manifest in a few lines.
func SaveCluster(clusterDir string, m *ClusterManifest) error {
	if m == nil {
		return errors.New("config.SaveCluster: nil ClusterManifest")
	}
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.LastModifiedAt = now
	if m.SchemaVersion == 0 {
		m.SchemaVersion = CurrentSchemaVersion
	}
	target := filepath.Join(clusterDir, ClusterFilename)
	return saveJSONAtomic(target, m)
}

// LoadGlobal reads the global config file at path. If path doesn't
// exist, returns (nil, nil) — a missing global config is normal
// and explicitly not an error per SPEC §3.3.
func LoadGlobal(path string) (*Global, error) {
	var g Global
	if err := loadJSONStrict(path, &g); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if g.SchemaVersion > CurrentSchemaVersion {
		return nil, fmt.Errorf("%s: schemaVersion %d > supported %d (upgrade pg_sandbox): %w",
			path, g.SchemaVersion, CurrentSchemaVersion, ErrSchemaVersionTooNew)
	}
	return &g, nil
}

// SaveSandbox atomically writes s to <sandboxDir>/SandboxFilename.
// The directory must already exist — Save does NOT create it,
// because that would mask a typo'd path. The deploy command is the
// only thing that creates sandbox dirs; everything else expects
// them to be there.
//
// On every save, LastModifiedAt is set to now. CreatedAt is
// preserved if non-zero, otherwise set to now (first save).
func SaveSandbox(sandboxDir string, s *Sandbox) error {
	if s == nil {
		return errors.New("config.SaveSandbox: nil Sandbox")
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.LastModifiedAt = now
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSchemaVersion
	}
	target := filepath.Join(sandboxDir, SandboxFilename)
	return saveJSONAtomic(target, s)
}

// SaveGlobal atomically writes g to path. The parent directory is
// created if missing — unlike sandbox dirs, the global config
// directory ($XDG_CONFIG_HOME/pg_sandbox/) is fine to create
// implicitly because there's nothing else in it the user might
// have meant by a typo.
func SaveGlobal(path string, g *Global) error {
	if g == nil {
		return errors.New("config.SaveGlobal: nil Global")
	}
	if g.SchemaVersion == 0 {
		g.SchemaVersion = CurrentSchemaVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config.SaveGlobal: %w", err)
	}
	return saveJSONAtomic(path, g)
}

// loadJSONStrict opens path, decodes JSON into out with
// DisallowUnknownFields, and returns a wrapping error on failure.
// Caller-visible error contains the path so users can fix the
// right file.
func loadJSONStrict(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err // os.PathError already includes the path
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// saveJSONAtomic marshals v as indented JSON and writes it to
// target via the temp+rename pattern.
//
// We use json.MarshalIndent (not a streaming Encoder) because
// configs are tiny (a few hundred bytes) and an in-memory buffer is
// simpler to reason about than a half-flushed Encoder on failure.
//
// The temp file is a sibling so the rename is guaranteed to be on
// the same filesystem (cross-fs rename is not atomic).
func saveJSONAtomic(target string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal %s: %w", target, err)
	}
	// Newline at EOF is a convention that makes the file pleasant
	// to cat and avoids "No newline at end of file" diffs.
	data = append(data, '\n')

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, filepath.Base(target)+".tmp.*")
	if err != nil {
		return fmt.Errorf("config: tempfile in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: write %s: %w", tmpName, err)
	}
	// fsync before rename — without it, on a crash between
	// rename-completion and the data hitting disk, we could end
	// up with a zero-length file pointed to by the canonical name.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("config: rename %s -> %s: %w", tmpName, target, err)
	}
	return nil
}

// IsSandboxDir reports whether dir contains the canonical sandbox
// config file. Used by start/stop/status/etc. to refuse to operate
// on a non-sandbox directory (SPEC §4.2).
func IsSandboxDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, SandboxFilename))
	return err == nil
}

// IsClusterDir reports whether dir contains the canonical cluster
// manifest file.
func IsClusterDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ClusterFilename))
	return err == nil
}

// GlobalConfigPath returns the default location of the global
// config file: $XDG_CONFIG_HOME/pg_sandbox/config.json, falling back
// to $HOME/.config/pg_sandbox/config.json. Returns an error if
// neither XDG_CONFIG_HOME nor HOME is set (rare but possible in
// restricted environments).
func GlobalConfigPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, GlobalDirname, GlobalFilename), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config.GlobalConfigPath: %w", err)
	}
	return filepath.Join(home, ".config", GlobalDirname, GlobalFilename), nil
}
