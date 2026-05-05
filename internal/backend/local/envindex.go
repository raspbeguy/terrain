package local

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// envIndex lists env-var names per workspace; values remain in the
// keyring. Needed because libsecret/Keychain have no portable List API,
// so we can't enumerate workspace env-vars from the keyring alone.
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
		_ = os.Remove(path)
		return nil
	}
	data, err := json.MarshalIndent(envIndex(names), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// addEnvVar is idempotent.
func addEnvVar(backendID, workspaceID, name string) error {
	names, _ := loadEnvIndex(backendID, workspaceID)
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return saveEnvIndex(backendID, workspaceID, append(names, name))
}

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
