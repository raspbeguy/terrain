// Package runner owns persistent run history. Backends call Record on every
// terminal state transition; the UI calls List to populate the Runs tab.
//
// Storage format: one ndjson file per (backend, workspace) under
// $XDG_CACHE_HOME/terrain/<backend>/<workspace>/runs.ndjson. ndjson is
// append-only and tolerates partial writes (a corrupt last line is just
// skipped on the next List), which fits the "one row per terminal run"
// access pattern. We don't need indexing yet — the typical workspace will
// have dozens to low-thousands of runs total.
package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
)

// HistoryEntry is one persisted run snapshot. Captures the immutable
// metadata (id/kind/created_at) plus the final status and artifact paths.
type HistoryEntry struct {
	ID           string           `json:"id"`
	WorkspaceID  string           `json:"workspace_id"`
	BackendID    string           `json:"backend_id"`
	Kind         domain.RunKind   `json:"kind"`
	Status       domain.RunStatus `json:"status"`
	Message      string           `json:"message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	PlanFile     string           `json:"plan_file,omitempty"`
	RunDir       string           `json:"run_dir,omitempty"`
	ExitCode     int              `json:"exit_code"`
	ErrorMessage string           `json:"error_message,omitempty"`
}

// History is a thread-safe append-only log of run snapshots.
type History struct {
	path string
	mu   sync.Mutex
}

// NewHistory opens (creates) the history file for one (backend, workspace)
// pair. The file is created lazily on first Record.
func NewHistory(backendID, workspaceID string) (*History, error) {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locate cache dir: %w", err)
	}
	dir := filepath.Join(cacheHome, "terrain", backendID, safePath(workspaceID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return &History{path: filepath.Join(dir, "runs.ndjson")}, nil
}

// Record appends a single entry. Caller is expected to call this once per
// run, on terminal status — but the API is forgiving: appending more than
// once for the same ID just lets List see multiple snapshots.
func (h *History) Record(e HistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", h.path, err)
	}
	defer f.Close()

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", h.path, err)
	}
	return nil
}

// List returns all entries in chronological order (oldest first). Corrupt
// lines are skipped; missing file returns an empty slice.
//
// For workspaces with thousands of runs we'd want to stream this — for
// now we read the file in full and parse line-by-line.
func (h *History) List() ([]HistoryEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	f, err := os.Open(h.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", h.path, err)
	}
	defer f.Close()

	var out []HistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e HistoryEntry
		if err := json.Unmarshal(line, &e); err != nil {
			// Skip corrupt entries silently — file may be mid-write or
			// from a different version; we don't want one bad row to nuke
			// the entire history view.
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("scan %s: %w", h.path, err)
	}
	return out, nil
}

// safePath turns an arbitrary workspace ID (which may contain colons,
// slashes, etc.) into a single safe filename component.
func safePath(id string) string {
	r := strings.NewReplacer("/", "_", ":", "_", "\\", "_", " ", "_")
	return r.Replace(id)
}
