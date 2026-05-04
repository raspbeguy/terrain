package local

import (
	"fmt"
	"os"
	"path/filepath"
)

// workspaceDataDir returns $XDG_DATA_HOME/terrain/<backend>/<sanitized-ws>/,
// creating it if missing. Holds terrain-managed per-workspace state that
// shouldn't live in the user's project tree (overrides.tfvars, env-vars.json,
// settings.json).
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

// containerPluginCacheDir is the per-workspace TF_PLUGIN_CACHE_DIR for
// container/bwrap modes. Kept separate from the host subprocess cache
// because container glibc/arch/lock-file hashes may diverge from the host's.
func containerPluginCacheDir(backendID, workspaceID string) (string, error) {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cache dir: %w", err)
	}
	dir := filepath.Join(cacheHome, "terrain", backendID, sanitize(workspaceID), "plugins-container")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return dir, nil
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
