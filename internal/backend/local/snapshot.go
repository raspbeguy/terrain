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

// Sits next to state.tfstate so listings need not re-parse the state.
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

// Best-effort: apply has already finished so failures only log.
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

	if err := b.pruneStateVersions(ws.ID, 50, 30*24*time.Hour); err != nil {
		slog.Warn("state-versions prune", "ws", ws.ID, "err", err)
	}
	return nil
}

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

// Keeps newest `keep` plus anything younger than maxAge.
func (b *Backend) pruneStateVersions(workspaceID string, keep int, maxAge time.Duration) error {
	versions, err := b.StateVersions(context.Background(), workspaceID)
	if err != nil {
		return err
	}
	if len(versions) <= keep {
		return nil
	}

	cutoff := time.Now().Add(-maxAge)
	for i, v := range versions {
		if i < keep {
			continue
		}
		if v.CreatedAt.After(cutoff) {
			continue
		}
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

// Skips dirs with missing/corrupt meta.json so one bad snapshot doesn't hide the rest.
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

// Avoids unmarshalling the multi-MB resource tree for two scalars.
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

// XDG_DATA_HOME (durable): snapshots outlive cache cleanups.
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

func newSnapshotID() string {
	return newRunID()
}
