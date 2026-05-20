package local

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func gitReposRoot() (string, error) {
	dataHome, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "terrain", "git-repos"), nil
}

func repoHash(gitURL, gitRef string) string {
	normalized := strings.TrimRight(gitURL, "/") + "@" + gitRef
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:16]
}

func CloneDir(gitURL, gitRef string) (string, error) {
	root, err := gitReposRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, repoHash(gitURL, gitRef)), nil
}

func gcOrphanClones(referenced map[string]bool) {
	root, err := gitReposRoot()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("git-repos gc readdir", "err", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if referenced[e.Name()] {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("git-repos gc remove", "path", path, "err", err)
			continue
		}
		slog.Info("removed orphan git clone", "hash", e.Name())
	}
}
