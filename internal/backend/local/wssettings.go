package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Zero value resolves to managed.
type BinarySource string

const (
	BinarySourceUnset   BinarySource = ""
	BinarySourceHost    BinarySource = "host"
	BinarySourceManaged BinarySource = "managed"
)

func (s BinarySource) Effective() BinarySource {
	if s == "" {
		return BinarySourceManaged
	}
	return s
}

// Zero value = inherit app defaults.
type WorkspaceSettings struct {
	BinarySource       BinarySource `json:"binary_source,omitempty"`
	ManagedEngine      string       `json:"managed_engine,omitempty"`
	ManagedVersion     string       `json:"managed_version,omitempty"`
	ManagedTrackLatest bool         `json:"managed_track_latest,omitempty"`
}

func (s WorkspaceSettings) EffectiveManagedEngine(appDefault string) string {
	if s.ManagedEngine != "" {
		return s.ManagedEngine
	}
	if appDefault != "" {
		return appDefault
	}
	return "tofu"
}

// Missing file returns the zero value; parse errors propagate.
func LoadWorkspaceSettings(backendID, workspaceID string) (WorkspaceSettings, error) {
	path, err := workspaceSettingsPath(backendID, workspaceID)
	if err != nil {
		return WorkspaceSettings{}, err
	}
	return loadWorkspaceSettingsAt(path)
}

func loadWorkspaceSettingsAt(path string) (WorkspaceSettings, error) {
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

// Zero-value s removes the file rather than persisting an empty marker.
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
