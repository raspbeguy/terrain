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

// bubblewrapRuntime sandboxes the host's tofu/terraform binary inside a
// bwrap user-namespace. Unlike containerRuntime there's no image system —
// the binary itself is the host's, just confined to a curated filesystem
// view. Strengths: starts in milliseconds, no daemon, available on every
// Flatpak host. Tradeoff: no version pin and no CI image parity — only
// the "isolation" motivation among the three the feature targets.
//
// Filesystem exposure (intentionally tight):
//
//   /usr             ro-bind from host (provides the binary + libs + CA certs)
//   /etc             ro-bind from host (DNS, ssl certs, /etc/passwd for getuid)
//   /lib /lib64      symlinked into /usr/lib{,64} for non-usrmerged distros
//   /bin /sbin       symlinked into /usr/bin / /usr/sbin
//   /proc            mounted procfs
//   /dev             dev-bind (limited dev nodes)
//   /tmp             tmpfs (per-run, gone on exit)
//   /workspace       bind from project dir (RW; tofu writes .terraform/, state)
//   /terrain/run     bind from per-run cache dir (RW; plan.tfplan, vars file)
//   /terrain/plugins bind from per-mode plugin cache (RW; provider downloads)
//
// Network is intentionally NOT isolated — providers need to reach their
// APIs. PID and UTS namespaces are unshared so the sandboxed process can't
// see / signal host processes. --die-with-parent ensures cleanup on crash.
type bubblewrapRuntime struct {
	bwrapBin        string
	pluginCacheHost string
	runDirHost      string
}

const (
	bwrapWorkdir   = "/workspace"
	bwrapRunDir    = "/terrain/run"
	bwrapPluginDir = "/terrain/plugins"
)

func (r bubblewrapRuntime) Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, _ string) *exec.Cmd {
	bwrapArgs := []string{
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/etc", "/etc",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}

	// Make sure the binary is reachable inside the sandbox. /usr/bin/tofu
	// is already covered by the /usr ro-bind; /usr/local/bin/tofu or a
	// custom install path needs an explicit single-file bind.
	if !strings.HasPrefix(bin, "/usr/") {
		bwrapArgs = append(bwrapArgs, "--ro-bind", bin, bin)
	}

	bwrapArgs = append(bwrapArgs,
		"--bind", workDir, bwrapWorkdir,
		"--bind", r.runDirHost, bwrapRunDir,
		"--bind", r.pluginCacheHost, bwrapPluginDir,
		"--chdir", bwrapWorkdir,
		"--unshare-pid",
		"--unshare-uts",
		"--new-session",
		"--die-with-parent",

		// Curated env. --clearenv wipes inherited host env; we re-set
		// just what tofu/terraform need so secrets in the host shell
		// (AWS_*, SSH_*, etc) don't leak into the sandbox unless the
		// user explicitly added them to the workspace's env-category
		// vars (which arrive via extraEnv below).
		"--clearenv",
		"--setenv", "PATH", "/usr/bin:/usr/sbin:/bin:/sbin",
		"--setenv", "HOME", "/tmp",
		"--setenv", "TF_PLUGIN_CACHE_DIR", bwrapPluginDir,
		"--setenv", "TF_IN_AUTOMATION", "1",
	)

	for _, kv := range extraEnv {
		eq := strings.Index(kv, "=")
		if eq <= 0 {
			continue
		}
		bwrapArgs = append(bwrapArgs, "--setenv", kv[:eq], kv[eq+1:])
	}

	bwrapArgs = append(bwrapArgs, "--", bin)

	mounts := map[string]string{
		workDir:      bwrapWorkdir,
		r.runDirHost: bwrapRunDir,
	}
	bwrapArgs = append(bwrapArgs, translateArgs(args, mounts)...)

	// bwrap itself runs through hostCommand so the Flatpak-->host
	// boundary still has exactly one chokepoint, mirroring how
	// containerRuntime invokes podman.
	return hostCommand(ctx, "", nil, r.bwrapBin, bwrapArgs...)
}

// Cancel for bwrap: nothing to do out-of-band. bwrap was started with
// --die-with-parent and inherits SIGINT via streamCommand's cmd.Cancel,
// which propagates to the sandboxed child through bwrap's own signal
// forwarding. No `kill --signal` equivalent needed.
func (r bubblewrapRuntime) Cancel(_ context.Context, _ string) error {
	return nil
}

// PullCommand: bwrap has no image system. Returns nil so the caller
// skips the pre-pull step entirely.
func (r bubblewrapRuntime) PullCommand(_ context.Context, _ string) *exec.Cmd {
	return nil
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
// concrete Runtime. Falls through to hostRuntime when the workspace
// prefers subprocess mode (or no override + global default is subprocess);
// returns containerRuntime / bubblewrapRuntime for the other modes. Errors
// when a sandboxed mode is requested but its prerequisites (runtime binary
// path, image) aren't satisfied — no silent fallback.
func newRuntime(opt runtimeOptions) (Runtime, error) {
	switch opt.RunMode {
	case RunModeContainer:
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
	case RunModeBubblewrap:
		bwrap, err := lookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("bubblewrap (bwrap) not found on PATH: %w", err)
		}
		return bubblewrapRuntime{
			bwrapBin:        bwrap,
			pluginCacheHost: opt.PluginCacheHost,
			runDirHost:      opt.RunDirHost,
		}, nil
	default:
		return hostRuntime{}, nil
	}
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
		case RunModeBubblewrap:
			mode = RunModeBubblewrap
		default:
			mode = RunModeSubprocess
		}
	}

	// Subprocess mode: no plugin cache or run-dir resolution needed; the
	// host's tofu reads .terraform/ directly out of the project tree.
	if mode == RunModeSubprocess {
		return runtimeOptions{RunMode: mode}, nil
	}

	// Bubblewrap and container modes both need the plugin cache + run dir
	// resolved up front so the runtime can plumb them through bind mounts.
	pluginCache, err := containerPluginCacheDir(b.id, workspaceID)
	if err != nil {
		return runtimeOptions{}, fmt.Errorf("plugin cache dir: %w", err)
	}
	runDir, err := runArtifactsDir(b.id, domain.Workspace{ID: workspaceID}, runID)
	if err != nil {
		return runtimeOptions{}, fmt.Errorf("run artifacts dir: %w", err)
	}

	if mode == RunModeBubblewrap {
		return runtimeOptions{
			RunMode:         mode,
			PluginCacheHost: pluginCache,
			RunDirHost:      runDir,
		}, nil
	}

	// Container mode: image resolution + runtime path on top of the above.

	image := ws.Image
	if image == "" {
		switch b.defaults.Engine {
		case "terraform":
			image = b.defaults.ImageTerraform
		default:
			image = b.defaults.ImageTofu
		}
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
