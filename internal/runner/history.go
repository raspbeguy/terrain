// Package runner: persistent run history (ndjson; corrupt lines skipped on List).
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

type History struct {
	path string
	mu   sync.Mutex
}

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
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("scan %s: %w", h.path, err)
	}
	return out, nil
}

func safePath(id string) string {
	r := strings.NewReplacer("/", "_", ":", "_", "\\", "_", " ", "_")
	return r.Replace(id)
}
