package local

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// envIndex is the on-disk record of which Environment-category variables
// exist for each workspace. Names only — values remain in the keyring. Lives
// at `<projectDir>/.terrain/env-vars.json` with shape:
//
//	{ "<workspace-id>": ["VAR1", "VAR2"] }
//
// Why a side file: keyring backends (libsecret, Keychain) don't expose a
// portable List API, so we can't enumerate "all env vars for this workspace"
// from the keyring alone. A small JSON index is the simplest fix.
type envIndex map[string][]string

func envIndexPath(projectDir string) string {
	return filepath.Join(projectDir, ".terrain", "env-vars.json")
}

func loadEnvIndex(projectDir, workspaceID string) ([]string, error) {
	data, err := os.ReadFile(envIndexPath(projectDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var idx envIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return idx[workspaceID], nil
}

func saveEnvIndex(projectDir, workspaceID string, names []string) error {
	path := envIndexPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var idx envIndex
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	if idx == nil {
		idx = envIndex{}
	}
	if len(names) == 0 {
		delete(idx, workspaceID)
	} else {
		idx[workspaceID] = names
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// addEnvVar appends name to the workspace's env-var index. Idempotent.
func addEnvVar(projectDir, workspaceID, name string) error {
	names, _ := loadEnvIndex(projectDir, workspaceID)
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return saveEnvIndex(projectDir, workspaceID, append(names, name))
}

// removeEnvVar drops name from the workspace's env-var index. Missing names
// are silently ignored.
func removeEnvVar(projectDir, workspaceID, name string) error {
	names, _ := loadEnvIndex(projectDir, workspaceID)
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return saveEnvIndex(projectDir, workspaceID, out)
}
