package local

import (
	"fmt"
	"os"
	"path/filepath"
)

// workspaceDataDir returns the absolute path of the per-workspace data
// directory under $XDG_DATA_HOME (or its default ~/.local/share). Layout:
//
//	$XDG_DATA_HOME/terrain/<backendID>/<sanitized-workspaceID>/
//
// This is the canonical location for all terrain-managed state that belongs
// to one workspace but doesn't live in libsecret — e.g. the overrides
// tfvars file and the env-category index. Keeping it out of the user's
// project directory prevents accidental commits of terrain-internal state.
//
// The directory is created on first use; callers can rely on it existing
// after a successful return.
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

// overridesPath is the per-workspace tfvars file holding terrain-managed
// non-sensitive variable values. Sensitive values stay in the keyring;
// env-category values stay in the keyring + env-vars.json index. The file
// is loaded with the workspace's other tfvars at run-materialize time and
// passed to tofu via -var-file= (highest precedence among file sources).
func overridesPath(backendID, workspaceID string) (string, error) {
	dir, err := workspaceDataDir(backendID, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "overrides.tfvars"), nil
}

// envIndexFile is the per-workspace JSON index of env-category variable
// names. Names only — values stay in libsecret. Replaces the old
// <projectDir>/.terrain/env-vars.json location so the project tree stays
// terrain-free.
func envIndexFile(backendID, workspaceID string) (string, error) {
	dir, err := workspaceDataDir(backendID, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "env-vars.json"), nil
}

// userDataDir resolves $XDG_DATA_HOME or its fallback. We don't use
// os.UserConfigDir here because we want data semantics (durable user state,
// survives reinstall) — config dir is for app preferences that re-derive
// cleanly from user choice.
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
