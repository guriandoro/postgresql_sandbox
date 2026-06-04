// On-disk schema types for pg_sandbox.
//
// This file is the single source of truth for what a sandbox config
// file and a cluster manifest contain on disk. SPEC §3 designed
// these fresh — they are deliberately not a port of the Python
// pg_sandbox.env shell format. Two things matter most about this
// schema:
//
//  1. Versioning. Every file carries a SchemaVersion. Readers
//     refuse files with SchemaVersion > CurrentSchemaVersion; that
//     way a newer pg_sandbox writer that adds a key in v2 doesn't
//     surprise an older binary into ignoring it.
//
//  2. Strictness. encoding/json's DisallowUnknownFields is used on
//     Load so a typo'd key (e.g. "datadir" vs "dataDir") becomes a
//     loud error rather than a silently-ignored value. Together
//     with omitempty on the optional sub-blocks (Physical/Logical),
//     this gives us "what you write is what you'll read back, no
//     hidden state" per SPEC §3.1.7.

package config

import "time"

// CurrentSchemaVersion is the highest schema version this binary
// understands. Bump when adding a breaking field; non-breaking
// additions (new optional fields) do NOT require a bump.
const CurrentSchemaVersion = 1

// Canonical filenames written under sandbox / cluster dirs. Defined
// as constants here so every code path agrees on the names.
const (
	// SandboxFilename is the per-sandbox config file. Its presence
	// in a directory is the definition of "this is a sandbox" per
	// SPEC §4.2.
	SandboxFilename = "pg_sandbox.json"

	// ClusterFilename is the cluster manifest. Its presence in a
	// directory is the definition of "this is a cluster".
	ClusterFilename = "pg_sandbox-cluster.json"

	// GlobalDirname is the subdirectory under $XDG_CONFIG_HOME (or
	// $HOME/.config) where the global config lives.
	GlobalDirname = "pg_sandbox"

	// GlobalFilename is the global config file basename.
	GlobalFilename = "config.json"
)

// Role enumerates the documented sandbox roles. Stored verbatim in
// JSON; new values may be added but never renamed (renames need a
// SchemaVersion bump and a Migrate step).
type Role string

const (
	RolePrimary    Role = "primary"
	RoleStandby    Role = "standby"
	RolePublisher  Role = "publisher"
	RoleSubscriber Role = "subscriber"
	// RoleUnknown is the default for freshly-created sandboxes
	// before deploy has finished classifying them.
	RoleUnknown Role = "unknown"
)

// SyncMode controls how a physical standby is registered on its
// primary's synchronous_standby_names list.
type SyncMode string

const (
	// SyncNone: standby is asynchronous (default).
	SyncNone SyncMode = "none"
	// SyncSync: standby is named as a synchronous member.
	SyncSync SyncMode = "sync"
	// SyncPotentialSync: standby is eligible but not currently
	// selected as synchronous (reserved for cluster sync-count
	// semantics).
	SyncPotentialSync SyncMode = "potential-sync"
)

// CopyMode controls subscription initial-data-copy behavior.
type CopyMode string

const (
	// CopyAll: WITH (copy_data = true) — default Postgres behavior.
	CopyAll CopyMode = "all"
	// CopySchema: caller ran pg_dump --schema-only first to seed
	// tables, then created the subscription with copy_data = true
	// for row data. Equivalent on the wire to CopyAll once the
	// subscription is created; preserved for inspectability.
	CopySchema CopyMode = "schema"
	// CopyNone: WITH (copy_data = false) — no initial snapshot.
	CopyNone CopyMode = "none"
)

// ClusterMode is the topology a cluster was created with.
type ClusterMode string

const (
	ClusterPhysical ClusterMode = "physical"
	ClusterLogical  ClusterMode = "logical"
)

// Sandbox is the on-disk schema for a single sandbox's config.
// Fields are JSON-tagged so the on-disk shape is stable even if Go
// names change. omitempty is reserved for genuinely optional
// blocks (Physical, Logical, Cluster); always-present fields are
// emitted even when zero so a hand-written file is readable.
type Sandbox struct {
	// SchemaVersion is the format version this file was written
	// with. See CurrentSchemaVersion.
	SchemaVersion int `json:"schemaVersion"`

	// Name is the sandbox's identifier within its sandbox root.
	// Used in slot names, application names, log messages.
	Name string `json:"name"`

	// BinDir is the absolute path to the PostgreSQL bin/ directory
	// this sandbox uses. Resolved once at deploy time and frozen
	// in the file; changing it later requires `config set binDir=`.
	BinDir string `json:"binDir"`

	// DataDir is the absolute path to the PostgreSQL data
	// directory (initdb's output). Typically inside the sandbox
	// dir, e.g. "/sandboxes/pg16/data".
	DataDir string `json:"dataDir"`

	// LogFile is the absolute path to the server.log written by
	// pg_ctl start -l.
	LogFile string `json:"logFile"`

	// Host is the address the server binds to and clients connect
	// to. Almost always 127.0.0.1 — this tool's scope is local.
	Host string `json:"host"`

	// Port is the TCP port. Set by the caller of deploy, validated
	// at write time to be 1..65535.
	Port int `json:"port"`

	// Superuser is the PG superuser name. Defaults to "postgres".
	Superuser string `json:"superuser"`

	// DefaultDatabase is what `use` / `run` connect to when -d is
	// not supplied. Defaults to "postgres".
	DefaultDatabase string `json:"defaultDatabase"`

	// Role classifies the sandbox. Set by deploy and possibly
	// updated by promote.
	Role Role `json:"role"`

	// Cluster names the cluster this sandbox belongs to, or empty
	// for a standalone sandbox.
	Cluster string `json:"cluster,omitempty"`

	// Physical, if set, captures physical-replication metadata.
	// Nil for primaries that have no parent and for non-physical
	// roles.
	Physical *Physical `json:"physical,omitempty"`

	// Logical, if set, captures logical-replication metadata.
	// Nil unless the sandbox is a subscriber.
	Logical *Logical `json:"logical,omitempty"`

	// CreatedAt is when the sandbox was first deployed. Written
	// once and never modified after creation.
	CreatedAt time.Time `json:"createdAt"`

	// LastModifiedAt is updated on every Save.
	LastModifiedAt time.Time `json:"lastModifiedAt"`
}

// Physical is the per-sandbox metadata block for physical streaming
// replication. Stored only on standbys; primaries with replicas
// don't carry their replica list here (that's the cluster
// manifest's job).
type Physical struct {
	// SourceSandbox is the name of the upstream this standby was
	// based on. May refer to another standby (cascading).
	SourceSandbox string `json:"sourceSandbox"`

	// SlotName is the physical replication slot on the source
	// that this standby consumes. Per SPEC §6.1, required when
	// --replicate-from is used.
	SlotName string `json:"slotName"`

	// ReplicationUser is the PG role used for the replication
	// connection. Conventionally "replicator".
	ReplicationUser string `json:"replicationUser"`

	// SyncMode controls whether this standby is registered as
	// synchronous on the source.
	SyncMode SyncMode `json:"syncMode"`

	// AppName is the application_name the standby reports to
	// pg_stat_replication. Defaults to the sandbox name.
	AppName string `json:"appName"`
}

// Logical is the per-sandbox metadata block for logical
// replication. Stored only on subscribers; the publication state
// lives on the publisher's Sandbox config (publication name(s)) and
// is queried via pg_publication at status time.
type Logical struct {
	// SourceSandbox names the publisher.
	SourceSandbox string `json:"sourceSandbox"`

	// PublicationName is what this subscriber attached to.
	PublicationName string `json:"publicationName"`

	// SubscriptionName is the CREATE SUBSCRIPTION identifier.
	SubscriptionName string `json:"subscriptionName"`

	// CopyMode records the initial-copy policy chosen at create
	// time. Subsequent restarts don't re-copy; this is for
	// inspectability.
	CopyMode CopyMode `json:"copyMode"`

	// TargetDatabase is the database on this sandbox that the
	// subscription was created in. May differ from
	// DefaultDatabase if the user passed --dbname explicitly to
	// subscribe.
	TargetDatabase string `json:"targetDatabase"`
}

// Global is the host-wide config that applies across all sandboxes.
// It is OPTIONAL — the tool works without it. Per SPEC §3.3, every
// field defaults to "unset" (meaning "fall back to built-in
// defaults"), so omitempty is appropriate everywhere.
type Global struct {
	// SchemaVersion is the format version this file was written
	// with. Same gate as Sandbox.SchemaVersion.
	SchemaVersion int `json:"schemaVersion"`

	// SandboxRoot overrides the default location for new sandboxes
	// (~/postgresql-sandboxes/).
	SandboxRoot string `json:"sandboxRoot,omitempty"`

	// DefaultBinDir is the convenience default for --bin-dir.
	DefaultBinDir string `json:"defaultBinDir,omitempty"`

	// PgGatherDir is the location of the pg_gather scripts used by
	// the report command.
	PgGatherDir string `json:"pgGatherDir,omitempty"`

	// DefaultPortBase overrides the default starting port for
	// auto-allocation (65432).
	DefaultPortBase int `json:"defaultPortBase,omitempty"`

	// DefaultPortRange overrides the default number of ports
	// scanned during auto-allocation (100).
	DefaultPortRange int `json:"defaultPortRange,omitempty"`
}

// ClusterManifest is the on-disk schema for a cluster's metadata.
// Stored at <cluster-dir>/pg_sandbox-cluster.json. Foundation phase
// defines the types; load/save and the cluster commands themselves
// land later.
type ClusterManifest struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Name           string          `json:"name"`
	Mode           ClusterMode     `json:"mode"`
	Members        []ClusterMember `json:"members"`
	Replication    ClusterRepl     `json:"replication"`
	CreatedAt      time.Time       `json:"createdAt"`
	LastModifiedAt time.Time       `json:"lastModifiedAt"`
}

// ClusterMember is one entry in ClusterManifest.Members.
type ClusterMember struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
	// SyncIndex is non-nil when this member is one of the first
	// --sync-count synchronous members. Nil for async members.
	// Pointer (rather than int with 0 as "unset") to keep "first
	// sync slot" (index 0) distinguishable from "not sync".
	SyncIndex *int `json:"syncIndex,omitempty"`
}

// ClusterRepl carries the cluster-level replication parameters.
type ClusterRepl struct {
	// SlotPrefix is the prefix used for physical slot names
	// (e.g. mycluster_s1_slot). Empty for logical clusters.
	SlotPrefix string `json:"slotPrefix,omitempty"`

	// PublicationName is the cluster-wide publication (logical
	// clusters only).
	PublicationName string `json:"publicationName,omitempty"`

	// SyncCount is the number of first members marked synchronous.
	SyncCount int `json:"syncCount"`
}
