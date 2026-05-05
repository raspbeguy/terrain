package domain

import "time"

// Workspace is a unit of state. For TFE/HCP/OTF it's a first-class API
// object; for local it's a project directory + a tofu workspace name.
type Workspace struct {
	// ID is unique within its Backend. Local format:
	// "<backend-id>:<project-id>:<ws-name>". Remote: the API ID.
	ID        string
	BackendID string
	Name      string
	// ProjectName / ProjectID identify the parent grouping (registered
	// project for local; TFE project / organization for remote).
	ProjectName string
	ProjectID   string
	// WorkingDirectory is empty for the project root.
	WorkingDirectory string
	// TerraformVersion is empty when undeclared (local auto-detects).
	TerraformVersion string
	// ExecutionMode mirrors TFE ("local"/"remote"/"agent"); local
	// backend always reports "local".
	ExecutionMode string
	Locked        bool
	LockedBy      string
	LockedAt      time.Time
	Description   string
}

type ProjectChoice struct {
	ID   string
	Name string
}
