// Migration from the legacy Python pg_sandbox.env format.
//
// SPEC §3.1.9 requires a one-shot reader that converts the
// shell-style KEY=VALUE files produced by the Python tool into a
// Sandbox struct that can be SaveSandbox'd next to (or in place of)
// the old file. This function is consumed by the future `config
// migrate` subcommand and is here in the foundation phase so
// command implementers don't need to revisit it.
//
// The legacy format is plain shell `export`-able lines:
//
//   PGS_BIN_DIR=/opt/postgresql/16.2
//   PGS_DATADIR=data
//   PGS_PORT=65432
//   ...
//
// Migration deliberately does NOT execute the file as a shell
// script (no `source`); we parse the literal text. That sidesteps
// the risk of someone shipping a weaponized env file that runs
// arbitrary commands when read.

package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Migrate reads a legacy pg_sandbox.env file at legacyPath and
// returns a Sandbox struct populated with whatever fields the
// legacy file specified. The sandbox directory is inferred from
// legacyPath's parent so relative-path values (PGS_DATADIR, PGS_LOG)
// can be resolved to absolute paths required by the new schema.
//
// Fields the legacy file doesn't specify are left zero — the
// caller should overlay Defaults() and then Validate() before
// saving.
//
// Migrate does not write anything to disk. Caller decides where
// the resulting Sandbox is persisted (typically pg_sandbox.json
// next to the original .env).
func Migrate(legacyPath string) (*Sandbox, error) {
	f, err := os.Open(legacyPath)
	if err != nil {
		return nil, fmt.Errorf("config.Migrate: %w", err)
	}
	defer f.Close()

	kv, err := parseLegacyEnv(f)
	if err != nil {
		return nil, fmt.Errorf("config.Migrate %s: %w", legacyPath, err)
	}

	sandboxDir := filepath.Dir(legacyPath)
	s := Sandbox{SchemaVersion: CurrentSchemaVersion}

	// Best-effort field mapping. Anything we don't recognize is
	// preserved in the returned struct as a zero/unset value;
	// Validate will tell the caller what's still missing.
	if v, ok := kv["PGS_BIN_DIR"]; ok {
		s.BinDir = v
	}
	if v, ok := kv["PGS_HOST"]; ok {
		s.Host = v
	}
	if v, ok := kv["PGS_PORT"]; ok {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("config.Migrate %s: PGS_PORT %q: %w", legacyPath, v, err)
		}
		s.Port = p
	}
	if v, ok := kv["PGS_USER"]; ok {
		s.Superuser = v
	}
	if v, ok := kv["PGS_DBNAME"]; ok {
		s.DefaultDatabase = v
	}
	if v, ok := kv["PGS_DATADIR"]; ok {
		// Legacy stored relative basenames; resolve to absolute.
		s.DataDir = absoluteOrJoin(sandboxDir, v)
	}
	if v, ok := kv["PGS_LOG"]; ok {
		s.LogFile = absoluteOrJoin(sandboxDir, v)
	}
	if v, ok := kv["PGS_ROLE"]; ok {
		s.Role = Role(strings.ToLower(strings.TrimSpace(v)))
	}
	if v, ok := kv["PGS_CLUSTER"]; ok {
		s.Cluster = v
	}

	// Sandbox name from the directory's basename — the legacy
	// format didn't store it explicitly.
	s.Name = filepath.Base(sandboxDir)

	// Physical replication block.
	if src, ok := kv["PGS_REPLICATE_FROM"]; ok && src != "" {
		s.Physical = &Physical{
			SourceSandbox:   src,
			SlotName:        kv["PGS_SLOT_NAME"],
			ReplicationUser: orDefault(kv["PGS_REPL_USER"], "replicator"),
			SyncMode:        SyncNone, // legacy file doesn't say; user can fix via config set
			AppName:         s.Name,
		}
	}

	// Logical replication block.
	if src, ok := kv["PGS_SUBSCRIBE_FROM"]; ok && src != "" {
		s.Logical = &Logical{
			SourceSandbox:    src,
			PublicationName:  kv["PGS_PUBLICATION_NAME"],
			SubscriptionName: kv["PGS_SUBSCRIPTION_NAME"],
			CopyMode:         CopyAll, // legacy file doesn't record; safe default
			TargetDatabase:   orDefault(kv["PGS_SUBSCRIPTION_DBNAME"], s.DefaultDatabase),
		}
	}

	return &s, nil
}

// parseLegacyEnv reads shell-style KEY=VALUE lines. Comments
// (starting with #), blank lines, and `export ` prefixes are
// tolerated. Values may be quoted with single or double quotes; we
// strip exactly one outer pair if present.
//
// We intentionally do NOT support backslash escapes, variable
// expansion (`$FOO`), or backticks. The legacy file is plain data;
// if a user manually added shell features we'd rather error than
// pretend to interpret them.
func parseLegacyEnv(r interface {
	Read(p []byte) (int, error)
}) (map[string]string, error) {
	kv := map[string]string{}
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: no '=' separator: %q", lineNo, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = unquote(val)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNo)
		}
		kv[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, errors.New("scan: " + err.Error())
	}
	return kv, nil
}

// unquote strips exactly one pair of outer single or double quotes
// from s if present. We do not interpret escape sequences inside —
// any backslash is preserved verbatim. This matches the legacy
// Python writer's behavior (it doesn't escape, so we don't either).
func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if first == last && (first == '"' || first == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// absoluteOrJoin returns v if it's already absolute, otherwise
// joins it with base. We don't os.Stat the result — Validate will
// catch dangling paths.
func absoluteOrJoin(base, v string) string {
	if filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(base, v)
}

// orDefault returns s if non-empty, otherwise fallback.
func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
