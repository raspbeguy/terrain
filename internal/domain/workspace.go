package domain

import "time"

// Local ID format: "<backend-id>:<project-id>:<ws-name>".
type Workspace struct {
	ID               string
	BackendID        string
	Name             string
	ProjectName      string
	ProjectID        string
	WorkingDirectory string
	GitURL           string
	GitRef           string
	Subpath          string
	TerraformVersion string
	ExecutionMode    string
	Locked           bool
	LockedBy         string
	LockedAt         time.Time
	Description      string
}

type ProjectChoice struct {
	ID   string
	Name string
}
