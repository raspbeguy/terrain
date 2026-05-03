package local

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// envIndex is the on-disk record of which Environment-category variables
// exist for a workspace. Names only — values remain in the keyring. Lives
// at $XDG_DATA_HOME/terrain/<backendID>/<sanitized-ws>/env-vars.json with
// shape:
//
//	["VAR1", "VAR2"]
//
// Why a side file: keyring backends (libsecret, Keychain) don't expose a
// portable List API, so we can't enumerate "all env vars for this workspace"
// from the keyring alone. A small JSON index is the simplest fix.
//
// Why per-workspace under XDG_DATA_HOME (not under the project directory):
// keeping it out of the user's project tree avoids accidental commits of
// terrain-internal state, and lets the user delete + re-clone their project
// without losing terrain's view of which env vars apply to that workspace.
type envIndex []string

func loadEnvIndex(backendID, workspaceID string) ([]string, error) {
	path, err := envIndexFile(backendID, workspaceID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
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
	return idx, nil
}

func saveEnvIndex(backendID, workspaceID string, names []string) error {
	path, err := envIndexFile(backendID, workspaceID)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		// No entries: remove the file rather than leaving an empty array.
		// Best-effort — failure to remove an absent file is fine.
		_ = os.Remove(path)
		return nil
	}
	data, err := json.MarshalIndent(envIndex(names), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// addEnvVar appends name to the workspace's env-var index. Idempotent.
func addEnvVar(backendID, workspaceID, name string) error {
	names, _ := loadEnvIndex(backendID, workspaceID)
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return saveEnvIndex(backendID, workspaceID, append(names, name))
}

// removeEnvVar drops name from the workspace's env-var index. Missing names
// are silently ignored.
func removeEnvVar(backendID, workspaceID, name string) error {
	names, _ := loadEnvIndex(backendID, workspaceID)
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return saveEnvIndex(backendID, workspaceID, out)
}
