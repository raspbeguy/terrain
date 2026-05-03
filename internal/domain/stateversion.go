package domain

import "time"

// StateVersion is a snapshot of a workspace's state taken after a
// successful apply (or imported from a remote backend's state-versions
// API). Mirrors TFE's state-versions concept: stable ID, the serial number
// from terraform itself, lineage UUID, timestamp, and the run that
// produced the snapshot.
//
// For local backends, RawPath / JSONPath point at on-disk files we
// persist under XDG_DATA_HOME. For remote backends, the paths are empty
// and the SizeBytes / DownloadURL fields drive on-demand fetches.
type StateVersion struct {
	// ID is unique within (BackendID, WorkspaceID). We use a sortable
	// time-prefixed identifier so on-disk listing reads chronological.
	ID string

	BackendID   string
	WorkspaceID string

	// Serial mirrors terraform.tfstate's `serial` field — a per-workspace
	// monotonic version counter. A serial that didn't increase means a
	// no-op or a re-init.
	Serial int64

	// Lineage is the per-state-tree UUID terraform writes to detect when
	// state has been wiped + re-initialised. A lineage change between two
	// snapshots is a strong signal something dramatic happened (terraform
	// state rm followed by import, or `tofu init -reconfigure`).
	Lineage string

	// CreatedAt is the local time the snapshot was written. For remote
	// backends this is what the API returns.
	CreatedAt time.Time

	// RunID is the run that produced this snapshot, when known.
	RunID string

	// RawPath points to the original .tfstate binary (the same format
	// terraform writes). Empty when not persisted (remote backends).
	RawPath string

	// JSONPath points to the parsed `tofu show -json` output of the same
	// state. Empty when not persisted.
	JSONPath string

	// SHA256 is a content hash of the raw .tfstate, used to detect
	// duplicates / verify retention rotation.
	SHA256 string
}
