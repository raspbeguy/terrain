package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RunMode determines how a workspace's runs are launched. Default is empty
// string which the resolver interprets as "fall back to the app-wide default
// in AppConfig.DefaultRunMode" — that way a brand-new workspace inherits the
// user's preference without us having to write a settings file on first
// access.
type RunMode string

const (
	// RunModeUnset means "use the global default". Persisted as the empty
	// string so a missing settings.json file is indistinguishable from a
	// freshly-saved one with no per-workspace override.
	RunModeUnset RunMode = ""

	// RunModeSubprocess invokes tofu/terraform directly on the host (the
	// pre-existing behaviour, what every workspace gets by default).
	RunModeSubprocess RunMode = "subprocess"

	// RunModeContainer launches each run inside a container, bind-mounting
	// the project dir + per-run cache dir. See runtime.go for the wiring.
	RunModeContainer RunMode = "container"

	// RunModeBubblewrap launches each run under bubblewrap (bwrap), the
	// same low-level user-namespace sandboxer Flatpak uses internally.
	// Uses the host's tofu/terraform binary (no image system) but
	// confines its filesystem view to /usr + the explicitly-bound
	// project/run/cache dirs. Lighter than container mode and starts in
	// milliseconds; doesn't help with version-pin or CI-parity, just
	// isolation. See runtime.go for the wiring.
	RunModeBubblewrap RunMode = "bubblewrap"
)

// WorkspaceSettings is the per-workspace user preference set saved next to
// overrides.tfvars / env-vars.json. None of these fields are required — a
// zero value means "follow the app-wide defaults."
//
// Why a separate file (vs extending domain.Workspace.ExecutionMode): the
// domain field is a TFE-mirror used by the remote backend too. Run-mode is
// a local-backend implementation choice and belongs here.
type WorkspaceSettings struct {
	// RunMode chooses subprocess vs container. Empty = inherit global.
	RunMode RunMode `json:"run_mode,omitempty"`

	// Image is the container image reference (with optional digest). Only
	// honoured when RunMode resolves to container. Empty = inherit the
	// engine-specific global default (tofu image vs terraform image).
	Image string `json:"image,omitempty"`
}

// LoadWorkspaceSettings reads the per-workspace settings.json. A missing
// file is not an error — returns zero-value settings, which the resolver
// treats as "fall back to global defaults". JSON parse errors propagate so
// the UI can surface them; we don't silently overwrite a corrupt file.
func LoadWorkspaceSettings(backendID, workspaceID string) (WorkspaceSettings, error) {
	path, err := workspaceSettingsPath(backendID, workspaceID)
	if err != nil {
		return WorkspaceSettings{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return WorkspaceSettings{}, nil
		}
		return WorkspaceSettings{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s WorkspaceSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return WorkspaceSettings{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// SaveWorkspaceSettings writes settings to the per-workspace settings.json
// using a temp-file + rename so a partial write or crash never leaves a
// corrupt file. If s is the zero value, the file is removed instead — keeps
// the data dir clean of empty-marker files.
func SaveWorkspaceSettings(backendID, workspaceID string, s WorkspaceSettings) error {
	path, err := workspaceSettingsPath(backendID, workspaceID)
	if err != nil {
		return err
	}
	if (s == WorkspaceSettings{}) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "settings.*.json.tmp")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	return os.Rename(tmpPath, path)
}

func workspaceSettingsPath(backendID, workspaceID string) (string, error) {
	dir, err := workspaceDataDir(backendID, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}
