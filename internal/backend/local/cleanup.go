package local

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// CleanupOrphanArtifacts walks this backend's run-artifact directory and
// deletes any leftover sensitive files from runs that didn't complete
// cleanly (app crashed, OOM kill, SIGKILL).
//
// Specifically: every `vars.auto.tfvars` (or its old `.json` sibling
// from before the HCL switch) under
// $XDG_CACHE_HOME/terrain/<backend>/<workspace>/runs/<run>/ — these are
// written 0600 with materialised secret values and normally deleted via
// `defer os.Remove` when the run worker exits. If the process dies before
// the defer runs, the file lingers.
//
// Called once at app startup per local backend. Best-effort: failures are
// logged but never propagated; we never want a stale-file-cleanup to block
// the app from launching.
//
// Single-instance assumption: this app isn't designed to run two copies
// concurrently. If it ever needs to, replace this with per-pid tracking
// (write a sibling .pid file at materialise time, only delete files whose
// pid is no longer alive).
func (b *Backend) CleanupOrphanArtifacts() {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return
	}
	root := filepath.Join(cacheHome, "terrain", b.id)
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return // nothing to clean
	}

	patterns := []string{
		filepath.Join(root, "*", "runs", "*", "vars.auto.tfvars"),
		filepath.Join(root, "*", "runs", "*", "vars.auto.tfvars.json"),
	}
	var matches []string
	for _, p := range patterns {
		m, err := filepath.Glob(p)
		if err != nil {
			slog.Warn("orphan cleanup glob", "err", err)
			continue
		}
		matches = append(matches, m...)
	}

	cleaned := 0
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			slog.Warn("orphan cleanup", "path", path, "err", err)
			continue
		}
		cleaned++
	}
	if cleaned > 0 {
		slog.Info("cleaned up orphan vars files from prior crashes",
			"backend", b.id, "count", cleaned)
	}
}
