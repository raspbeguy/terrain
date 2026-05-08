package local

import "regexp"

var workspaceNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// IsValidWorkspaceName mirrors tofu's own check (terraform/internal/command/workspace_command.go).
func IsValidWorkspaceName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	return workspaceNameRE.MatchString(name)
}
