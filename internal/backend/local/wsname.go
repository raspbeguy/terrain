package local

import "regexp"

var workspaceNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Mirrors tofu's own check.
func IsValidWorkspaceName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	return workspaceNameRE.MatchString(name)
}
