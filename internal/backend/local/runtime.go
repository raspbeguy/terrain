package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
)

// Runtime abstracts how a single tofu/terraform invocation actually runs:
// directly on the host, or inside a container. Both implementations
// ultimately invoke `hostCommand` in sandbox.go so Flatpak's
// `flatpak-spawn --host` chokepoint is preserved (one for the binary in
// host mode, one for the container runtime in container mode).
//
// The split lives here rather than inside hostCommand because path
// translation for `-out=` / `-var-file=` / positional plan files is a
// terraform-CLI argument-shape concern; sandbox.go shouldn't know about
// those flags.
type Runtime interface {
	// Command builds an *exec.Cmd that runs `args` against `bin` (the
	// resolved tofu/terraform binary path) inside `workDir`. extraEnv is
	// appended to the process environment as KEY=VAL strings. cancelName,
	// when non-empty, is the unique identifier the cancel hook uses to
	// signal the running process — currently a per-run string like
	// "terrain-<runID>" that the container runtime sets via `--name`.
	Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, cancelName string) *exec.Cmd

	// Cancel delivers a SIGINT-equivalent to the running command identified
	// by cancelName. Called from cmd.Cancel as the first attempt before
	// signalling the wrapper process, which gives container runtimes a
	// chance to forward SIGINT through their PID-1 init. Returning nil
	// when the runtime doesn't need a separate kill path is fine.
	Cancel(ctx context.Context, cancelName string) error

	// PullCommand returns an *exec.Cmd that pulls the image. Streams
	// through the caller's existing log pipeline (streamCommand) so pull
	// progress appears in the run log view. Host runtime returns nil
	// (no-op). The returned command is short-lived: if the image is
	// already local, `podman pull` exits in <1s with "Image already
	// exists" output.
	PullCommand(ctx context.Context, image string) *exec.Cmd
}

// hostRuntime is the original behaviour: invoke `bin args...` directly via
// `hostCommand`. Cancellation is handled entirely by the parent layer
// (cmd.Cancel sends SIGINT to the spawned process).
type hostRuntime struct{}

func (hostRuntime) Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, _ string) *exec.Cmd {
	return hostCommand(ctx, workDir, extraEnv, bin, args...)
}

func (hostRuntime) Cancel(_ context.Context, _ string) error {
	return nil // cmd.Cancel SIGINT is sufficient for direct subprocesses.
}

func (hostRuntime) PullCommand(_ context.Context, _ string) *exec.Cmd {
	return nil
}

// containerRuntime wraps tofu invocations in `<runtimeBin> run --rm --init`
// with bind mounts for the workspace + run cache + a per-mode plugin
// cache. The user-configured runtime binary path comes from AppConfig
// (default `/usr/bin/podman`, but anything `podman`-compatible works:
// docker, nerdctl, finch).
type containerRuntime struct {
	// runtimeBin is the resolved path to podman / docker / equivalent.
	runtimeBin string

	// image is the container image to run (e.g.
	// "ghcr.io/opentofu/opentofu:1.7" or with a digest suffix).
	image string

	// pluginCacheHost is the host path bind-mounted as TF_PLUGIN_CACHE_DIR
	// inside the container. Per-workspace, per-mode (container).
	pluginCacheHost string

	// runDirHost is the host path bind-mounted at /terrain/run.
	runDirHost string

	// rootless flips to true when we're using rootless podman with the
	// `keep-id` user-namespace mapping. In that case we omit `--user`
	// because keep-id handles UID translation implicitly and a literal
	// `--user` flag conflicts. Detected once at runtime construction.
	rootless bool
}

const (
	// containerWorkdir is where the workspace's project directory is
	// bind-mounted inside the container. Tofu CDs into this dir.
	containerWorkdir = "/workspace"

	// containerRunDir is where per-run artifacts (vars.auto.tfvars, plan.tfplan)
	// live inside the container. Bind-mounted RW so plan output persists
	// back to the host run cache.
	containerRunDir = "/terrain/run"

	// containerPluginDir mounts the per-workspace, per-mode plugin cache
	// into the container so providers persist across runs without
	// re-downloading on every init.
	containerPluginDir = "/terrain/plugins"
)

// Command builds a `<runtimeBin> run ...` invocation. Path-shaped args are
// rewritten via translateArgs before being appended. extraEnv entries are
// passed via repeated `-e KEY=VAL` flags rather than relying on the
// runtime's host-env inheritance (which doesn't propagate inside the
// container by default).
func (r containerRuntime) Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, cancelName string) *exec.Cmd {
	mounts := map[string]string{
		workDir:       containerWorkdir,
		r.runDirHost:  containerRunDir,
		// plugin-cache mount is set via -v below; not a path-translation
		// target since no CLI arg names it directly.
	}

	runArgs := []string{
		"run", "--rm", "--init",
		"--name", cancelName,
		"--workdir", containerWorkdir,
		"-v", workDir + ":" + containerWorkdir + ":rw,Z",
		"-v", r.runDirHost + ":" + containerRunDir + ":rw,Z",
		"-v", r.pluginCacheHost + ":" + containerPluginDir + ":rw,Z",
		"-e", "TF_PLUGIN_CACHE_DIR=" + containerPluginDir,
		"-e", "TF_IN_AUTOMATION=1",
	}
	for _, kv := range extraEnv {
		runArgs = append(runArgs, "-e", kv)
	}
	if !r.rootless {
		// Rootful podman / docker: explicitly map UID/GID so bind-mount
		// writes are owned by the host user, not root. Rootless podman
		// with keep-id does this implicitly; passing --user there
		// conflicts.
		runArgs = append(runArgs, "--user", currentUserSpec())
	}
	runArgs = append(runArgs, r.image)

	// `bin` here is the host-side path to tofu/terraform discovered via
	// lookPath. Inside the container we want `tofu` or `terraform` from
	// the image's PATH; pass the leaf basename, not the host absolute
	// path (which doesn't exist inside the container).
	runArgs = append(runArgs, basename(bin))

	runArgs = append(runArgs, translateArgs(args, mounts)...)

	// Container runtime invocation itself routes through hostCommand so
	// Flatpak's `flatpak-spawn --host` wrapping still applies — we want
	// the runtime binary to run on the host, not in the Flatpak sandbox.
	// extraEnv on this OUTER hostCommand is empty: we already injected
	// the env via -e flags, and the container inherits its own env, not
	// the runtime binary's host env.
	return hostCommand(ctx, "", nil, r.runtimeBin, runArgs...)
}

// Cancel sends SIGINT to the in-container PID 1 (which is tini via --init,
// which forwards to tofu) using the runtime's `kill` subcommand. Called
// from cmd.Cancel before signalling the wrapper process — the
// belt-and-suspenders pattern guards against signal-forwarding bugs in
// rootless podman + flatpak-spawn pipelines.
func (r containerRuntime) Cancel(ctx context.Context, cancelName string) error {
	if cancelName == "" {
		return errors.New("cancel name required")
	}
	cmd := hostCommand(ctx, "", nil, r.runtimeBin, "kill", "--signal", "INT", cancelName)
	// Don't care about output — best-effort. If the container is already
	// gone, kill returns non-zero; that's fine, the caller will fall
	// through to signalling the wrapper process.
	_ = cmd.Run()
	return nil
}

// PullCommand returns the `<runtimeBin> pull <image>` invocation, ready to
// be run through streamCommand so progress appears in the run log view.
// Caller decides whether to await it or skip — both podman and docker
// short-circuit when the image is already local.
func (r containerRuntime) PullCommand(ctx context.Context, image string) *exec.Cmd {
	return hostCommand(ctx, "", nil, r.runtimeBin, "pull", image)
}

// translateArgs rewrites host paths embedded in CLI arguments to their
// in-container equivalents based on the bind-mount map. Pure function so
// it's table-testable. Handles three shapes the local backend emits via
// buildCmdArgs:
//
//   - flag=value form (`-out=/host/path/plan.tfplan`)
//   - bare positional path (`apply <plan-file>`)
//   - non-path args (passes through unchanged)
//
// `mounts` maps host prefix → container prefix; longest-prefix match wins
// so a more-specific mount supersedes a parent.
func translateArgs(args []string, mounts map[string]string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-out=") {
			out = append(out, "-out="+translatePath(a[len("-out="):], mounts))
			continue
		}
		if strings.HasPrefix(a, "-var-file=") {
			out = append(out, "-var-file="+translatePath(a[len("-var-file="):], mounts))
			continue
		}
		// Positional absolute path — the apply subcommand takes the plan
		// file as a bare arg. Any other absolute path arg gets the same
		// treatment defensively.
		if strings.HasPrefix(a, "/") {
			out = append(out, translatePath(a, mounts))
			continue
		}
		out = append(out, a)
	}
	return out
}

// translatePath rewrites one absolute path according to the mount map.
// Falls back to the original path when no mount applies — that lets a
// stray absolute path (e.g. an env-vars-injected file path that happens
// to be inside a bind mount we don't know about) surface as a clear
// "file not found" inside the container rather than a silent corruption.
func translatePath(p string, mounts map[string]string) string {
	var bestHost, bestContainer string
	for host, container := range mounts {
		if !strings.HasPrefix(p, host) {
			continue
		}
		if len(host) > len(bestHost) {
			bestHost = host
			bestContainer = container
		}
	}
	if bestHost == "" {
		return p
	}
	rest := strings.TrimPrefix(p, bestHost)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return bestContainer
	}
	return bestContainer + "/" + rest
}

// installRuntimeCancel installs a Cancel hook on cmd that asks the runtime
// to deliver SIGINT to the named container before streamCommand falls
// through to signalling the wrapper process. Belt-and-suspenders for
// flatpak-spawn → rootless-podman → tini → tofu signal chains where any
// link can drop the signal.
func installRuntimeCancel(cmd *exec.Cmd, rt Runtime, cancelName string) {
	cmd.Cancel = func() error {
		// Best-effort: 3s timeout so a stuck runtime can't wedge cancel.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = rt.Cancel(ctx, cancelName)
		return nil // streamCommand chains and falls through to SIGINT.
	}
}

func basename(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// currentUserSpec renders the host user as `UID:GID` for podman/docker's
// --user flag. Falls back to "0:0" only as a last resort, which means
// container files would be root-owned on bind mount — bad UX, but better
// than refusing to run when somehow the UID/GID lookup fails.
func currentUserSpec() string {
	uid := os.Getuid()
	gid := os.Getgid()
	return fmt.Sprintf("%d:%d", uid, gid)
}

// newRuntime resolves the per-workspace settings + global config into a
// concrete Runtime. Returns hostRuntime when the workspace prefers
// subprocess mode (or no override + global default is subprocess);
// containerRuntime otherwise. Errors when container mode is requested but
// the runtime binary path doesn't resolve — no silent fallback.
func newRuntime(opt runtimeOptions) (Runtime, error) {
	if opt.RunMode != RunModeContainer {
		return hostRuntime{}, nil
	}
	if opt.RuntimeBin == "" {
		return nil, errors.New("container run mode selected but ContainerRuntimePath is empty in Preferences")
	}
	bin, err := lookPath(opt.RuntimeBin)
	if err != nil {
		return nil, fmt.Errorf("container runtime %q not found: %w", opt.RuntimeBin, err)
	}
	if opt.Image == "" {
		return nil, errors.New("container run mode selected but no image is configured (workspace setting or default for engine)")
	}
	return containerRuntime{
		runtimeBin:      bin,
		image:           opt.Image,
		pluginCacheHost: opt.PluginCacheHost,
		runDirHost:      opt.RunDirHost,
		rootless:        detectRootlessPodman(bin),
	}, nil
}

// runtimeOptions bundles the inputs newRuntime needs. Keeps the
// constructor's signature stable as we add more knobs (digest pinning,
// extra mounts, etc).
type runtimeOptions struct {
	RunMode         RunMode
	RuntimeBin      string // user-configured path; "podman" or "docker" or absolute
	Image           string
	PluginCacheHost string
	RunDirHost      string
}

// resolveRuntimeOptions merges per-workspace settings, the backend's
// runtime defaults, and per-run cache paths into a concrete runtimeOptions
// suitable for newRuntime. Used by runWorker before the run starts.
//
// Resolution rules:
//   - RunMode: workspace setting wins; otherwise backend default; otherwise
//     subprocess.
//   - Image: workspace setting wins; otherwise the engine-specific default
//     (ImageTofu vs ImageTerraform based on backend Engine).
//   - Image is only required when the resolved RunMode is container —
//     subprocess runs ignore it entirely.
func (b *Backend) resolveRuntimeOptions(workspaceID, runID string) (runtimeOptions, error) {
	ws, err := LoadWorkspaceSettings(b.id, workspaceID)
	if err != nil {
		return runtimeOptions{}, fmt.Errorf("load workspace settings: %w", err)
	}

	mode := ws.RunMode
	if mode == RunModeUnset {
		switch RunMode(b.defaults.RunMode) {
		case RunModeContainer:
			mode = RunModeContainer
		default:
			mode = RunModeSubprocess
		}
	}

	if mode != RunModeContainer {
		return runtimeOptions{RunMode: mode}, nil
	}

	image := ws.Image
	if image == "" {
		switch b.defaults.Engine {
		case "terraform":
			image = b.defaults.ImageTerraform
		default:
			image = b.defaults.ImageTofu
		}
	}

	pluginCache, err := containerPluginCacheDir(b.id, workspaceID)
	if err != nil {
		return runtimeOptions{}, fmt.Errorf("plugin cache dir: %w", err)
	}
	runDir, err := runArtifactsDir(b.id, domain.Workspace{ID: workspaceID}, runID)
	if err != nil {
		return runtimeOptions{}, fmt.Errorf("run artifacts dir: %w", err)
	}

	return runtimeOptions{
		RunMode:         mode,
		RuntimeBin:      b.defaults.RuntimePath,
		Image:           image,
		PluginCacheHost: pluginCache,
		RunDirHost:      runDir,
	}, nil
}

// detectRootlessPodman is a best-effort check: if the runtime binary's
// basename is "podman" AND `podman info` reports rootless mode, we skip
// the explicit --user flag (keep-id handles UID translation implicitly).
// All other runtimes (docker, nerdctl, finch, rootful podman) need
// --user. Cached per-process to avoid re-running `info` on every command.
var rootlessOnce struct {
	once sync.Once
	v    bool
}

func detectRootlessPodman(bin string) bool {
	rootlessOnce.once.Do(func() {
		if !strings.HasSuffix(bin, "podman") {
			rootlessOnce.v = false
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := hostCommand(ctx, "", nil, bin, "info", "--format", "{{.Host.Security.Rootless}}").Output()
		if err != nil {
			rootlessOnce.v = false
			return
		}
		rootlessOnce.v = strings.TrimSpace(string(out)) == "true"
	})
	return rootlessOnce.v
}
