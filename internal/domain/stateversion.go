package domain

import "time"

// StateVersion is a snapshot of a workspace's state, mirroring TFE's
// state-versions concept. RawPath / JSONPath are populated for local
// backends; remote backends leave them empty and fetch on demand.
type StateVersion struct {
	// ID is sortable + time-prefixed so on-disk listing is chronological.
	ID          string
	BackendID   string
	WorkspaceID string
	// Serial mirrors terraform.tfstate's monotonic counter.
	Serial int64
	// Lineage is terraform's per-state-tree UUID; a change signals a
	// state wipe + re-init (state rm + import, `tofu init -reconfigure`).
	Lineage   string
	CreatedAt time.Time
	RunID     string
	// RawPath / JSONPath are the on-disk .tfstate and `show -json`
	// outputs. Empty for remote backends.
	RawPath  string
	JSONPath string
	// SHA256 is the content hash of the raw .tfstate; used for dedup
	// and retention verification.
	SHA256 string
}
