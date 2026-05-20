package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
)

func writeFakeSnapshot(t *testing.T, backendID, workspaceID, id string, age time.Duration, lineage string, serial int64) {
	t.Helper()
	dir, err := stateVersionDir(backendID, workspaceID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := snapshotMeta{
		ID:          id,
		BackendID:   backendID,
		WorkspaceID: workspaceID,
		Serial:      serial,
		Lineage:     lineage,
		CreatedAt:   time.Now().Add(-age),
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"format_version":"1.0","values":{"root_module":{"resources":[]}}}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPruneStateVersions_KeepNewest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	b := New("ret-test", "Local")
	const wsID = "ret-test:p1:default"

	for i := 1; i <= 5; i++ {
		writeFakeSnapshot(t, b.id, wsID, "snap-"+strconv.Itoa(i),
			time.Duration(60-i)*time.Hour,
			"L", int64(i))
	}

	for i := 1; i <= 5; i++ {
		writeFakeSnapshot(t, b.id, wsID, "old-"+strconv.Itoa(i),
			time.Duration(31*24+i)*time.Hour,
			"L", int64(100+i))
	}

	if err := b.pruneStateVersions(wsID, 2, 30*24*time.Hour); err != nil {
		t.Fatalf("prune: %v", err)
	}

	versions, _ := b.StateVersions(context.Background(), wsID)
	if len(versions) != 5 {
		t.Errorf("expected 5 survivors, got %d: %+v", len(versions), idsOf(versions))
	}
	for _, v := range versions {
		if v.ID[:5] != "snap-" {
			t.Errorf("unexpected survivor: %s", v.ID)
		}
	}
}

func TestPruneStateVersions_KeepWithinAge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	b := New("ret-test", "Local")
	const wsID = "ret-test:p1:default"

	writeFakeSnapshot(t, b.id, wsID, "fresh-1", 1*time.Hour, "L", 1)
	writeFakeSnapshot(t, b.id, wsID, "fresh-2", 2*time.Hour, "L", 2)
	writeFakeSnapshot(t, b.id, wsID, "fresh-3", 3*time.Hour, "L", 3)
	writeFakeSnapshot(t, b.id, wsID, "fresh-4", 4*time.Hour, "L", 4)

	if err := b.pruneStateVersions(wsID, 1, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	versions, _ := b.StateVersions(context.Background(), wsID)
	if len(versions) != 4 {
		t.Errorf("expected all 4 to survive (within maxAge), got %d", len(versions))
	}
}

func TestPruneStateVersions_DeletesBeyondBoth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	b := New("ret-test", "Local")
	const wsID = "ret-test:p1:default"

	writeFakeSnapshot(t, b.id, wsID, "fresh", 30*time.Minute, "L", 100)
	for i := 1; i <= 5; i++ {
		writeFakeSnapshot(t, b.id, wsID, "old-"+strconv.Itoa(i),
			time.Duration(48+i)*time.Hour, "L", int64(i))
	}

	if err := b.pruneStateVersions(wsID, 1, 1*time.Hour); err != nil {
		t.Fatal(err)
	}

	versions, _ := b.StateVersions(context.Background(), wsID)
	if len(versions) != 1 {
		t.Fatalf("expected only fresh to survive, got %d: %+v", len(versions), idsOf(versions))
	}
	if versions[0].ID != "fresh" {
		t.Errorf("expected fresh, got %s", versions[0].ID)
	}
}

func TestLoadStateVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	b := New("lsv-test", "Local")
	const wsID = "lsv-test:p1:default"

	writeFakeSnapshot(t, b.id, wsID, "v1", 1*time.Hour, "L", 5)

	state, err := b.LoadStateVersion(context.Background(), wsID, "v1")
	if err != nil {
		t.Fatalf("LoadStateVersion: %v", err)
	}
	if state == nil {
		t.Fatal("nil state")
	}
	if state.Values == nil || state.Values.RootModule == nil {
		t.Errorf("unexpected state shape: %+v", state)
	}
}

func TestLoadStateVersion_Missing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	b := New("lsv-test", "Local")
	if _, err := b.LoadStateVersion(context.Background(), "nope", "missing"); err == nil {
		t.Fatal("expected error for missing version")
	}
}

func idsOf(vs []domain.StateVersion) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}