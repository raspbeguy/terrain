package local

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// inFlatpakOnce caches the /.flatpak-info probe — we'll be asked many times
// per run (every binary detect, every command construction).
var inFlatpakOnce struct {
	sync.Once
	v bool
}

// inFlatpak reports whether terrain is running inside a Flatpak sandbox.
// Detection is the standard /.flatpak-info marker — present in every
// flatpak-bundled app, absent on the host. Caches the result.
func inFlatpak() bool {
	inFlatpakOnce.Do(func() {
		_, err := os.Stat("/.flatpak-info")
		inFlatpakOnce.v = err == nil
	})
	return inFlatpakOnce.v
}

// hostCommand builds an *exec.Cmd that runs `name args...` on the host when
// in a Flatpak sandbox (via `flatpak-spawn --host`), or directly otherwise.
// workDir becomes the process cwd; extraEnv entries are prepended on top of
// the host's natural environment (NOT mixed with the sandbox's os.Environ()
// — passing the sandbox's HOME / XDG_* would point host tofu at the wrong
// dirs).
//
// Caller must NOT set cmd.Dir / cmd.Env after this returns; both are baked
// into the command construction.
func hostCommand(ctx context.Context, workDir string, extraEnv []string, name string, args ...string) *exec.Cmd {
	if inFlatpak() {
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

// lookPath resolves a binary name to an absolute path. Inside Flatpak it asks
// the host (`flatpak-spawn --host sh -c 'command -v <name>'`) so we discover
// the user's host-installed tofu/terraform; outside, it's a thin wrapper on
// exec.LookPath.
//
// The host path string is informational from the sandbox's perspective — we
// can't dlopen or stat it from inside the sandbox, but we can pass it back to
// flatpak-spawn for execution and we can display it in Preferences.
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

