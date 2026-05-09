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

// writeFakeSnapshot drops a snapshot dir with the specified ID, age, and
// lineage. Used to seed retention + lineage-warning tests without invoking
// the tofu binary.
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
	// Also write a minimal state.json so LoadStateVersion can read it.
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

	// Seed 5 snapshots, all OLDER than maxAge (so age-based keep doesn't apply).
	for i := 1; i <= 5; i++ {
		writeFakeSnapshot(t, b.id, wsID, "snap-"+strconv.Itoa(i),
			time.Duration(60-i)*time.Hour, // 59h, 58h, ..., 55h ago; all old
			"L", int64(i))
	}

	// Keep 2 newest, age cutoff 30 days. The 5 are all "older than 30 days"?
	// No: they're 55-59 hours old, NOT older than 30 days. So age-based
	// keep applies → all kept. To exercise the prune we need ages > 30 days.
	for i := 1; i <= 5; i++ {
		writeFakeSnapshot(t, b.id, wsID, "old-"+strconv.Itoa(i),
			time.Duration(31*24+i)*time.Hour, // > 30 days
			"L", int64(100+i))
	}

	if err := b.pruneStateVersions(wsID, 2, 30*24*time.Hour); err != nil {
		t.Fatalf("prune: %v", err)
	}

	versions, _ := b.StateVersions(context.Background(), wsID)
	// Newest 2 (snap-5 and snap-4, youngest of the recent batch) are kept.
	// All 5 in the recent batch are within 30 days, so they ALL stay.
	// All 5 of the "old-*" are older than 30 days; only first 2 (newest beyond keep) stay if within keep, but they're at index 5+ which is > keep=2.
	// Actually: StateVersions returns newest first; the 5 "snap-" are at indexes 0-4 (newest), then 5 "old-" at 5-9 (oldest).
	// Keep=2 → indexes 0,1 (snap-5, snap-4) unconditionally kept.
	// Indexes 2-9: kept only if within cutoff. snap-1..3 are within (55-59h ago < 30d). old-* are beyond.
	// So expected: snap-1..5 (5 entries) survive, all old-* deleted.

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

	// 4 recent snapshots, all younger than maxAge; keep=1 means by count
	// only the newest survives, but the maxAge clause keeps the others too.
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

	// 1 fresh + 5 ancient. Keep=1, maxAge=1h → only fresh survives.
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