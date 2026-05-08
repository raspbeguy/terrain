package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSettings_RoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const (
		backend = "local"
		ws      = "local:p1:default"
	)

	got, err := LoadWorkspaceSettings(backend, ws)
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if (got != WorkspaceSettings{}) {
		t.Errorf("expected zero value, got %+v", got)
	}

	want := WorkspaceSettings{
		BinarySource:  BinarySourceManaged,
		ManagedEngine: "tofu",
	}
	if err := SaveWorkspaceSettings(backend, ws, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = LoadWorkspaceSettings(backend, ws)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}

	if err := SaveWorkspaceSettings(backend, ws, WorkspaceSettings{}); err != nil {
		t.Fatalf("Save zero: %v", err)
	}
	path, _ := workspaceSettingsPath(backend, ws)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected settings file to be removed, stat err = %v", err)
	}
}

func TestWorkspaceSettings_CorruptFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const (
		backend = "local"
		ws      = "ws-bad"
	)
	path, err := workspaceSettingsPath(backend, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadWorkspaceSettings(backend, ws)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}
