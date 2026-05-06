package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var inFlatpakOnce struct {
	sync.Once
	v bool
}

func inFlatpak() bool {
	inFlatpakOnce.Do(func() {
		_, err := os.Stat("/.flatpak-info")
		inFlatpakOnce.v = err == nil
	})
	return inFlatpakOnce.v
}

// hostCommand: callers must not set cmd.Dir / cmd.Env after this returns.
func hostCommand(ctx context.Context, workDir string, extraEnv []string, name string, args ...string) *exec.Cmd {
	if inFlatpak() && !runnableInSandbox(name) {
		spawn := []string{"--host"}
		if workDir != "" {
			spawn = append(spawn, "--directory="+workDir)
		}
		for _, e := range extraEnv {
			spawn = append(spawn, "--env="+e)
		}
		spawn = append(spawn, name)
		spawn = append(spawn, args...)
		return exec.CommandContext(ctx, "flatpak-spawn", spawn...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd
}

// runnableInSandbox: bare command names (no slash) are treated as host-only.
func runnableInSandbox(name string) bool {
	if !filepath.IsAbs(name) {
		return false
	}
	info, err := os.Stat(name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// lookPath asks the host via flatpak-spawn when sandboxed; falls through to
// exec.LookPath otherwise. The returned path is informational from inside
// the sandbox but executable through flatpak-spawn.
func lookPath(name string) (string, error) {
	if !inFlatpak() {
		return exec.LookPath(name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx,
		"flatpak-spawn", "--host", "sh", "-c", "command -v "+name).Output()
	if err != nil {
		return "", exec.ErrNotFound
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", exec.ErrNotFound
	}
	return p, nil
}

