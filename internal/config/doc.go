// Package config implements the configuration subsystem: per-sandbox
// and global config schema, atomic load/save, layered resolution
// (flag > env > sandbox file > global file > default), validation,
// and migration from the legacy Python pg_sandbox.env format.
//
// This is the most important package to design carefully — see
// SPEC.md §3 for the design rationale and required properties.
package config
