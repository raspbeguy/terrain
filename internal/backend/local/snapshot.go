package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
)

// snapshotMeta is the on-disk shape we write next to state.tfstate /
// state.json so the listing path can pull metadata without re-parsing the
// state binary every time.
type snapshotMeta struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	BackendID   string    `json:"backend_id"`
	Serial      int64     `json:"serial"`
	Lineage     string    `json:"lineage"`
	CreatedAt   time.Time `json:"created_at"`
	RunID       string    `json:"run_id,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
}

// snapshotState writes a state-version directory after a successful apply.
// Best-effort: failures are logged at the call site but don't fail the run
// — the apply already finished. The snapshot directory layout is:
//
//	$XDG_DATA_HOME/terrain/<backend>/<workspace>/state-versions/<id>/
//	  state.tfstate    raw binary (when readable from project dir)
//	  state.json       tofu show -json output
//	  meta.json        snapshotMeta
//
// id is time-prefixed hex so on-disk listings sort chronologically (newest
// first when reversed).
func (b *Backend) snapshotState(ctx context.Context, ws domain.Workspace, runID string) error {
	bin, err := DetectBinary()
	if err != nil {
		return fmt.Errorf("detect binary: %w", err)
	}

	showCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	jsonOut, err := runShowJSON(showCtx, bin.Path, ws.WorkingDirectory)
	if err != nil {
		return fmt.Errorf("show -json: %w", err)
	}

	rawPath := filepath.Join(ws.WorkingDirectory, "terraform.tfstate")
	rawData, rawErr := os.ReadFile(rawPath)
	if rawErr != nil && !errors.Is(rawErr, fs.ErrNotExist) {
		// Other read errors (permission etc.) get logged but we proceed —
		// the JSON copy alone is still useful.
		slog.Warn("read raw tfstate", "path", rawPath, "err", rawErr)
		rawData = nil
	}

	serial, lineage := extractSerialLineage(rawData)
	id := newSnapshotID()

	dir, err := stateVersionDir(b.id, ws.ID, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if rawData != nil {
		// 0600 — same as the live state file's effective sensitivity.
		if err := os.WriteFile(filepath.Join(dir, "state.tfstate"), rawData, 0o600); err != nil {
			slog.Warn("persist raw state", "err", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), jsonOut, 0o600); err != nil {
		return fmt.Errorf("write state.json: %w", err)
	}

	meta := snapshotMeta{
		ID:          id,
		WorkspaceID: ws.ID,
		BackendID:   b.id,
		Serial:      serial,
		Lineage:     lineage,
		CreatedAt:   time.Now(),
		RunID:       runID,
		SHA256:      sha256Hex(rawData),
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), metaJSON, 0o644); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}
	slog.Info("state snapshot written", "ws", ws.ID, "id", id, "serial", serial, "lineage", lineage)

	// Best-effort retention sweep: prune older snapshots so the dir doesn't
	// grow without bound. Defaults match the plan: keep last 50 plus
	// anything from the last 30 days.
	if err := b.pruneStateVersions(ws.ID, 50, 30*24*time.Hour); err != nil {
		slog.Warn("state-versions prune", "ws", ws.ID, "err", err)
	}
	return nil
}

// LoadStateVersion reads a specific snapshot's state.json and returns the
// parsed *tfjson.State. The caller already knows the version ID from a
// previous StateVersions() call.
func (b *Backend) LoadStateVersion(_ context.Context, workspaceID, versionID string) (*tfjson.State, error) {
	dir, err := stateVersionDir(b.id, workspaceID, versionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, fmt.Errorf("read state.json for version %s: %w", versionID, err)
	}
	var state tfjson.State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode state version %s: %w", versionID, err)
	}
	return &state, nil
}

// pruneStateVersions enforces the retention policy: keep the newest `keep`
// snapshots plus anything younger than maxAge. Older snapshots beyond both
// thresholds are deleted (entire snapshot dir).
//
// Errors on individual deletes are logged + skipped so a partial cleanup
// doesn't surface as a hard failure.
func (b *Backend) pruneStateVersions(workspaceID string, keep int, maxAge time.Duration) error {
	versions, err := b.StateVersions(context.Background(), workspaceID)
	if err != nil {
		return err
	}
	if len(versions) <= keep {
		return nil // nothing to prune
	}

	cutoff := time.Now().Add(-maxAge)
	for i, v := range versions {
		// Newest are at low indexes (StateVersions sorts newest first).
		if i < keep {
			continue
		}
		if v.CreatedAt.After(cutoff) {
			continue
		}
		// Beyond keep AND older than cutoff → drop.
		dir, err := stateVersionDir(b.id, workspaceID, v.ID)
		if err != nil {
			slog.Warn("compute prune dir", "id", v.ID, "err", err)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("prune state-version", "id", v.ID, "err", err)
			continue
		}
		slog.Debug("pruned state-version", "id", v.ID, "age", time.Since(v.CreatedAt))
	}
	return nil
}

// StateVersions reads all snapshots for a workspace, newest first. Skips
// directories with corrupt or missing meta.json — one bad snapshot
// shouldn't hide the rest.
func (b *Backend) StateVersions(_ context.Context, workspaceID string) ([]domain.StateVersion, error) {
	root, err := stateVersionsRoot(b.id, workspaceID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state-versions dir: %w", err)
	}

	var out []domain.StateVersion
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		metaData, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		var m snapshotMeta
		if err := json.Unmarshal(metaData, &m); err != nil {
			continue
		}
		out = append(out, domain.StateVersion{
			ID:          m.ID,
			BackendID:   m.BackendID,
			WorkspaceID: m.WorkspaceID,
			Serial:      m.Serial,
			Lineage:     m.Lineage,
			CreatedAt:   m.CreatedAt,
			RunID:       m.RunID,
			RawPath:     filepath.Join(dir, "state.tfstate"),
			JSONPath:    filepath.Join(dir, "state.json"),
			SHA256:      m.SHA256,
		})
	}
	// Newest first. CreatedAt comes from time.Now() at write — if two
	// snapshots happen in the same second (unlikely but possible), break
	// ties by ID lexicographically.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func runShowJSON(ctx context.Context, binPath, workDir string) ([]byte, error) {
	cmd := hostCommand(ctx, workDir, nil, binPath, "show", "-json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// extractSerialLineage parses the raw .tfstate JSON to pull out the two
// fields without unmarshalling the whole structure (which can be MB of
// resource attributes). Returns zero values when the input is empty or
// malformed.
func extractSerialLineage(raw []byte) (serial int64, lineage string) {
	if len(raw) == 0 {
		return 0, ""
	}
	var probe struct {
		Serial  int64  `json:"serial"`
		Lineage string `json:"lineage"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Serial, probe.Lineage
}

func sha256Hex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// stateVersionsRoot returns $XDG_DATA_HOME/terrain/<backend>/<ws>/state-versions/.
// State versions live under XDG_DATA_HOME (durable) rather than
// XDG_CACHE_HOME (where ephemeral runs go) — the snapshots are intended
// to outlive cache cleanups.
func stateVersionsRoot(backendID, workspaceID string) (string, error) {
	home, err := dataHome()
	if err != nil {
		return "", err
	}
	safeWS := sanitize(workspaceID)
	return filepath.Join(home, "terrain", backendID, safeWS, "state-versions"), nil
}

func stateVersionDir(backendID, workspaceID, snapshotID string) (string, error) {
	root, err := stateVersionsRoot(backendID, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, snapshotID), nil
}

// dataHome returns $XDG_DATA_HOME or its default. We don't use os.UserDataDir
// (added in Go 1.25) directly to keep build-time flexibility; the env var
// + fallback covers Linux + BSD + macOS-with-XDG-set, which is everything
// we ship on.
func dataHome() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".local", "share"), nil
}

// newSnapshotID returns a sortable, opaque identifier for one state
// snapshot. Time-prefixed so listing reads chronological without an index.
func newSnapshotID() string {
	// Reuse the run-ID generator — same shape works for both purposes.
	return newRunID()
}
