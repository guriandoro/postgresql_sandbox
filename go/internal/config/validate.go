// Schema validation.
//
// Validate runs after merging all layers (defaults + global + env +
// sandbox file + flags) and just before writing. Its job is to
// catch:
//
//   - Missing required fields (we have no good default — caller
//     forgot to fill them in).
//   - Out-of-range numeric values (port not in 1..65535).
//   - Enum values that aren't on the allow list (typo'd Role).
//   - Logical inconsistencies (Physical set but SourceSandbox empty;
//     Logical set but PublicationName empty).
//
// Validate does NOT consult the filesystem (no "does BinDir
// exist?" check). That's a deploy-time concern; here we only
// answer "is this struct internally well-formed?" so the same
// function works for in-memory configs that haven't been
// persisted yet.

package config

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ValidationError is returned by Validate. It carries every problem
// at once so the user fixes them in one pass instead of one error
// per re-run.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "config: " + e.Problems[0]
	}
	return fmt.Sprintf("config: %d problems: %v", len(e.Problems), e.Problems)
}

// Validate checks a Sandbox for required fields, value ranges, and
// internal consistency. Returns nil iff the config is safe to write.
func Validate(s *Sandbox) error {
	if s == nil {
		return errors.New("config.Validate: nil Sandbox")
	}
	var v ValidationError

	if s.SchemaVersion <= 0 {
		v.Problems = append(v.Problems,
			"schemaVersion must be > 0 (it's set automatically on save; pass through Defaults() if writing by hand)")
	}
	if s.SchemaVersion > CurrentSchemaVersion {
		v.Problems = append(v.Problems,
			fmt.Sprintf("schemaVersion %d is newer than this binary supports (%d)",
				s.SchemaVersion, CurrentSchemaVersion))
	}
	if s.Name == "" {
		v.Problems = append(v.Problems, "name must be non-empty")
	}
	if s.BinDir == "" {
		v.Problems = append(v.Problems, "binDir must be non-empty")
	} else if !filepath.IsAbs(s.BinDir) {
		v.Problems = append(v.Problems, "binDir must be an absolute path: "+s.BinDir)
	}
	if s.DataDir == "" {
		v.Problems = append(v.Problems, "dataDir must be non-empty")
	} else if !filepath.IsAbs(s.DataDir) {
		v.Problems = append(v.Problems, "dataDir must be an absolute path: "+s.DataDir)
	}
	if s.LogFile == "" {
		v.Problems = append(v.Problems, "logFile must be non-empty")
	} else if !filepath.IsAbs(s.LogFile) {
		v.Problems = append(v.Problems, "logFile must be an absolute path: "+s.LogFile)
	}
	if s.Host == "" {
		v.Problems = append(v.Problems, "host must be non-empty")
	}
	if s.Port < 1 || s.Port > 65535 {
		v.Problems = append(v.Problems,
			fmt.Sprintf("port must be in 1..65535, got %d", s.Port))
	}
	if s.Superuser == "" {
		v.Problems = append(v.Problems, "superuser must be non-empty")
	}
	if s.DefaultDatabase == "" {
		v.Problems = append(v.Problems, "defaultDatabase must be non-empty")
	}
	if !isValidRole(s.Role) {
		v.Problems = append(v.Problems,
			fmt.Sprintf("role %q is not a recognized value", string(s.Role)))
	}
	if s.Physical != nil {
		validatePhysical(s.Physical, &v)
	}
	if s.Logical != nil {
		validateLogical(s.Logical, &v)
	}
	if s.Role == RoleStandby && s.Physical == nil {
		v.Problems = append(v.Problems,
			"role=standby requires a physical{} block to be set")
	}
	if s.Role == RoleSubscriber && s.Logical == nil {
		v.Problems = append(v.Problems,
			"role=subscriber requires a logical{} block to be set")
	}

	if len(v.Problems) == 0 {
		return nil
	}
	return &v
}

// validatePhysical enforces internal consistency of the physical
// block.
func validatePhysical(p *Physical, v *ValidationError) {
	if p.SourceSandbox == "" {
		v.Problems = append(v.Problems, "physical.sourceSandbox must be non-empty")
	}
	if p.SlotName == "" {
		v.Problems = append(v.Problems, "physical.slotName must be non-empty")
	}
	if p.ReplicationUser == "" {
		v.Problems = append(v.Problems, "physical.replicationUser must be non-empty")
	}
	if !isValidSyncMode(p.SyncMode) {
		v.Problems = append(v.Problems,
			fmt.Sprintf("physical.syncMode %q is not recognized", string(p.SyncMode)))
	}
}

// validateLogical enforces internal consistency of the logical
// block.
func validateLogical(l *Logical, v *ValidationError) {
	if l.SourceSandbox == "" {
		v.Problems = append(v.Problems, "logical.sourceSandbox must be non-empty")
	}
	if l.PublicationName == "" {
		v.Problems = append(v.Problems, "logical.publicationName must be non-empty")
	}
	if l.SubscriptionName == "" {
		v.Problems = append(v.Problems, "logical.subscriptionName must be non-empty")
	}
	if !isValidCopyMode(l.CopyMode) {
		v.Problems = append(v.Problems,
			fmt.Sprintf("logical.copyMode %q is not recognized", string(l.CopyMode)))
	}
	if l.TargetDatabase == "" {
		v.Problems = append(v.Problems, "logical.targetDatabase must be non-empty")
	}
}

func isValidRole(r Role) bool {
	switch r {
	case RolePrimary, RoleStandby, RolePublisher, RoleSubscriber, RoleUnknown:
		return true
	}
	return false
}

func isValidSyncMode(m SyncMode) bool {
	switch m {
	case SyncNone, SyncSync, SyncPotentialSync:
		return true
	}
	return false
}

func isValidCopyMode(m CopyMode) bool {
	switch m {
	case CopyAll, CopySchema, CopyNone:
		return true
	}
	return false
}
