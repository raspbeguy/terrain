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

// Runtime is the strategy interface for how a single tofu/terraform
// invocation runs. Implementations build on top of hostCommand so the
// Flatpak chokepoint stays at one place.
type Runtime interface {
	Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, cancelName string) *exec.Cmd
	Cancel(ctx context.Context, cancelName string) error
	PullCommand(ctx context.Context, image string) *exec.Cmd
}

type hostRuntime struct{}

func (hostRuntime) Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, _ string) *exec.Cmd {
	return hostCommand(ctx, workDir, extraEnv, bin, args...)
}

func (hostRuntime) Cancel(_ context.Context, _ string) error                 { return nil }
func (hostRuntime) PullCommand(_ context.Context, _ string) *exec.Cmd        { return nil }

// containerRuntime invokes tofu through `<runtimeBin> run --rm --init`
// against an OCI image (podman, docker, nerdctl, finch).
type containerRuntime struct {
	runtimeBin      string
	image           string
	pluginCacheHost string
	runDirHost      string
	// rootless skips --user because rootless podman with keep-id maps
	// UIDs implicitly and a literal --user conflicts.
	rootless bool
}

const (
	containerWorkdir   = "/workspace"
	containerRunDir    = "/terrain/run"
	containerPluginDir = "/terrain/plugins"
)

func (r containerRuntime) Command(ctx context.Context, workDir string, extraEnv []string, bin string, args []string, cancelName string) *exec.Cmd {
	mounts := map[string]string{
		workDir:      containerWorkdir,
		r.runDirHost: containerRunDir,
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
		runArgs = append(runArgs, "--user", currentUserSpec())
	}
	runArgs = append(runArgs, r.image, basename(bin))
	runArgs = append(runArgs, translateArgs(args, mounts)...)
	return hostCommand(ctx, "", nil, r.runtimeBin, runArgs...)
}

func (r containerRuntime) Cancel(ctx context.Context, cancelName string) error {
	if cancelName == "" {
		return errors.New("cancel name required")
	}
	_ = hostCommand(ctx, "", nil, r.runtimeBin, "kill", "--signal", "INT", cancelName).Run()
	return nil
}

func (r containerRuntime) PullCommand(ctx context.Context, image string) *exec.Cmd {
	return hostCommand(ctx, "", nil, r.runtimeBin, "pull", image)
}

// translateArgs rewrites host paths embedded in CLI args to their in-
// sandbox equivalents using a host-prefix → sandbox-prefix map; longest
// matching prefix wins.
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

// bubblewrapRuntime sandboxes the host's tofu/terraform binary in a bwrap
// user namespace with a curated /usr + /etc + tmpfs view, plus binds for
// the workspace, run-cache, and plugin cache. Network is left intact so
// providers can reach their APIs; PID/UTS are unshared.
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

	// Custom install paths (/usr/local/, /opt/, ~/.local) need a single-
	// file bind; /usr/bin/tofu is already covered by the /usr ro-bind.
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
		// --clearenv prevents host shell secrets (AWS_*, SSH_*, …) from
		// leaking; what we need we re-inject below + via extraEnv.
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
	return hostCommand(ctx, "", nil, r.bwrapBin, bwrapArgs...)
}

func (r bubblewrapRuntime) Cancel(_ context.Context, _ string) error          { return nil }
func (r bubblewrapRuntime) PullCommand(_ context.Context, _ string) *exec.Cmd { return nil }

// installRuntimeCancel chains rt.Cancel onto cmd.Cancel so streamCommand
// runs the runtime-level kill (e.g. `podman kill --signal INT`) before
// SIGINT-ing the wrapper process.
func installRuntimeCancel(cmd *exec.Cmd, rt Runtime, cancelName string) {
	cmd.Cancel = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = rt.Cancel(ctx, cancelName)
		return nil
	}
}

func basename(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func currentUserSpec() string {
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

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

type runtimeOptions struct {
	RunMode         RunMode
	RuntimeBin      string
	Image           string
	PluginCacheHost string
	RunDirHost      string
}

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
	if mode == RunModeSubprocess {
		return runtimeOptions{RunMode: mode}, nil
	}

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

// detectRootlessPodman returns true when bin is rootless podman (in which
// case keep-id maps UIDs implicitly and a literal --user conflicts).
// docker / nerdctl / rootful podman get --user. Result is cached.
var rootlessOnce struct {
	once sync.Once
	v    bool
}

func detectRootlessPodman(bin string) bool {
	rootlessOnce.once.Do(func() {
		if !strings.HasSuffix(bin, "podman") {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := hostCommand(ctx, "", nil, bin, "info", "--format", "{{.Host.Security.Rootless}}").Output()
		if err != nil {
			return
		}
		rootlessOnce.v = strings.TrimSpace(string(out)) == "true"
	})
	return rootlessOnce.v
}
