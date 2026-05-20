package local

import (
	"fmt"
	"os"
	"path/filepath"
)

func workspaceDataDir(backendID, workspaceID string) (string, error) {
	dataHome, err := userDataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataHome, "terrain", backendID, sanitize(workspaceID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return dir, nil
}

func overridesPath(backendID, workspaceID string) (string, error) {
	dir, err := workspaceDataDir(backendID, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "overrides.tfvars"), nil
}

func envIndexFile(backendID, workspaceID string) (string, error) {
	dir, err := workspaceDataDir(backendID, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "env-vars.json"), nil
}

func userDataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share"), nil
}
