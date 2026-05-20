package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupOrphanArtifacts_RemovesOrphans(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	b := New("cleanup-test", "Local")

	want := []string{
		filepath.Join(dir, "terrain", b.id, "ws-a", "runs", "run-1", "vars.auto.tfvars.json"),
		filepath.Join(dir, "terrain", b.id, "ws-a", "runs", "run-2", "vars.auto.tfvars.json"),
		filepath.Join(dir, "terrain", b.id, "ws-b", "runs", "run-3", "vars.auto.tfvars.json"),
	}
	for _, p := range want {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"secret":"value"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	survivor := filepath.Join(dir, "terrain", b.id, "ws-a", "runs", "run-1", "stdout.log")
	if err := os.WriteFile(survivor, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}

	b.CleanupOrphanArtifacts()

	for _, p := range want {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected %s to be deleted, stat=%v", p, err)
		}
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("expected non-vars file %s to survive: %v", survivor, err)
	}
}

func TestCleanupOrphanArtifacts_NoCacheDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	b := New("never-ran", "Local")
	b.CleanupOrphanArtifacts()
}

func TestCleanupOrphanArtifacts_OnlyTouchesOwnBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	a := New("backend-a", "A")
	other := filepath.Join(dir, "terrain", "backend-b", "ws-x", "runs", "run-1", "vars.auto.tfvars.json")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	a.CleanupOrphanArtifacts()

	if _, err := os.Stat(other); err != nil {
		t.Errorf("backend-a's cleanup must not delete backend-b's files: %v", err)
	}
}
