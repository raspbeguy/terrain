package local

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// Best-effort: assumes single-instance; concurrent copies would need pid tracking.
func (b *Backend) CleanupOrphanArtifacts() {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return
	}
	root := filepath.Join(cacheHome, "terrain", b.id)
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return
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

	referenced := map[string]bool{}
	for _, p := range b.projects {
		referenced[repoHash(p.GitURL, p.GitRef)] = true
	}
	gcOrphanClones(referenced)
}
