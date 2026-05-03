package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractSerialLineage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		raw         string
		wantSerial  int64
		wantLineage string
	}{
		{
			name:        "valid",
			raw:         `{"version":4,"terraform_version":"1.6.0","serial":42,"lineage":"abc-123"}`,
			wantSerial:  42,
			wantLineage: "abc-123",
		},
		{
			name:        "missing fields",
			raw:         `{"version":4}`,
			wantSerial:  0,
			wantLineage: "",
		},
		{
			name:        "empty",
			raw:         "",
			wantSerial:  0,
			wantLineage: "",
		},
		{
			name:        "malformed",
			raw:         `not json`,
			wantSerial:  0,
			wantLineage: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, l := extractSerialLineage([]byte(tc.raw))
			if s != tc.wantSerial {
				t.Errorf("serial: got %d, want %d", s, tc.wantSerial)
			}
			if l != tc.wantLineage {
				t.Errorf("lineage: got %q, want %q", l, tc.wantLineage)
			}
		})
	}
}

func TestStateVersions_ListingAndSorting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	b := New("local-test", "Local")
	const wsID = "local-test:proj1:default"

	// Pre-populate three snapshots with different timestamps so we can
	// verify sort order. Older first to make sure the listing reverses.
	now := time.Now()
	want := []struct {
		id      string
		serial  int64
		lineage string
		created time.Time
	}{
		{id: "a-old", serial: 1, lineage: "L1", created: now.Add(-3 * time.Minute)},
		{id: "b-mid", serial: 2, lineage: "L1", created: now.Add(-2 * time.Minute)},
		{id: "c-new", serial: 3, lineage: "L2", created: now.Add(-1 * time.Minute)},
	}
	for _, w := range want {
		dir, err := stateVersionDir(b.id, wsID, w.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		meta := snapshotMeta{
			ID:          w.id,
			BackendID:   b.id,
			WorkspaceID: wsID,
			Serial:      w.serial,
			Lineage:     w.lineage,
			CreatedAt:   w.created,
		}
		mdata, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), mdata, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Plus one corrupt entry — it must be silently skipped.
	corrupt, _ := stateVersionDir(b.id, wsID, "broken")
	_ = os.MkdirAll(corrupt, 0o755)
	_ = os.WriteFile(filepath.Join(corrupt, "meta.json"), []byte("{not json"), 0o644)

	got, err := b.StateVersions(context.Background(), wsID)
	if err != nil {
		t.Fatalf("StateVersions: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 entries (corrupt skipped), got %d: %+v", len(got), got)
	}

	// Newest first: c-new, b-mid, a-old.
	wantOrder := []string{"c-new", "b-mid", "a-old"}
	for i, sv := range got {
		if sv.ID != wantOrder[i] {
			t.Errorf("order[%d]: got %q, want %q", i, sv.ID, wantOrder[i])
		}
	}

	// Verify path fields are populated relative to XDG_DATA_HOME.
	first := got[0]
	if first.JSONPath == "" || first.RawPath == "" {
		t.Errorf("expected paths populated, got %+v", first)
	}
	if !filepath.IsAbs(first.JSONPath) {
		t.Errorf("expected absolute path, got %q", first.JSONPath)
	}
}

func TestStateVersions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	b := New("local-test", "Local")
	got, err := b.StateVersions(context.Background(), "any-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestSha256Hex(t *testing.T) {
	t.Parallel()
	if got := sha256Hex(nil); got != "" {
		t.Errorf("nil → %q, want empty", got)
	}
	// Stable hash for "abc"
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := sha256Hex([]byte("abc")); got != want {
		t.Errorf("sha256(abc) = %q, want %q", got, want)
	}
}

func TestDataHome_RespectsEnv(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/path")
	got, err := dataHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/path" {
		t.Errorf("got %q, want /custom/path", got)
	}
}

func TestDataHome_FallbackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/test")
	got, err := dataHome()
	if err != nil {
		t.Fatal(err)
	}
	want := "/home/test/.local/share"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
