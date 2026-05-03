package domain

import "time"

// Workspace is a unit of state — local or remote. In TFE/HCP/OTF this is a
// first-class API object; for the local backend it's a logical pairing of a
// project directory + a Terraform CLI workspace name (`tofu workspace`).
type Workspace struct {
	// ID is unique within its Backend. For local: "<backend-id>:<project-id>:<ws-name>".
	// For remote: the backend's API ID.
	ID string

	// BackendID is the owning Backend.ID() — useful when workspaces from
	// multiple backends are flattened into one list (the sidebar).
	BackendID string

	// Name is the workspace name (e.g. "default", "production").
	Name string

	// ProjectName is the parent project / VCS repo / org. For local backends
	// this is the user-chosen display name of the project directory; for
	// remote backends it's the TFE project or organization.
	ProjectName string

	// ProjectID is the parent project's stable identifier. For local
	// backends it's the registered ProjectConfig.ID; for remote backends
	// it's the TFE project ID (or empty if the workspace isn't grouped
	// under a TFE project). Used by variable sets with project scope.
	ProjectID string

	// WorkingDirectory is the path (local) or relative subpath (remote) that
	// terraform/tofu runs against. Empty for the project root.
	WorkingDirectory string

	// TerraformVersion is the version constraint declared on the workspace.
	// Empty if undeclared (local backend will auto-detect).
	TerraformVersion string

	// ExecutionMode mirrors TFE: "local", "remote", "agent". Local backend
	// always reports "local".
	ExecutionMode string

	// Locked, plus who/when, mirrors TFE. Local backends use a filesystem
	// lock; an empty LockedBy means "not locked".
	Locked   bool
	LockedBy string
	LockedAt time.Time

	// Description is free-form user text shown in the workspace overview.
	Description string
}

// ProjectChoice is a tiny pair used to populate project-aware UI controls.
// Lives in domain so backend implementations and the UI dialogs can share
// the same type without an import dance.
type ProjectChoice struct {
	ID   string
	Name string
}
