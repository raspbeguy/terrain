// Package local implements domain.Backend against a local Terraform/OpenTofu
// working directory using the tofu/terraform CLI. M1 only supports listing
// registered projects as workspaces; run execution lands in M2, variables and
// state in M3.
package local

import (
	"context"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/runner"
)

// Project is one registered local project — a directory containing .tf files.
type Project struct {
	ID   string
	Name string
	Path string
}

// Backend exposes one or more local projects as a single domain.Backend. The
// rationale for grouping all local projects under a single backend rather
// than one backend per project: in the sidebar we want all local projects
// grouped together under a "Local" header — that maps cleanly to one Backend
// with multiple workspaces.
type Backend struct {
	id       string
	name     string
	projects []Project
	defaults RuntimeDefaults
}

// RuntimeDefaults captures the app-wide preferences the local backend
// needs to resolve a workspace's run mode + image when the per-workspace
// settings.json doesn't override them. Populated by the registry from
// AppConfig at backend construction time.
type RuntimeDefaults struct {
	// Engine selects the default container image when a workspace doesn't
	// pin one — "tofu" (default) maps to ImageTofu, "terraform" to
	// ImageTerraform.
	Engine string

	// RuntimePath is the path to the container CLI binary (podman, docker,
	// nerdctl, ...). Empty means container mode is unavailable.
	RuntimePath string

	// RunMode is the default for new workspaces ("subprocess" or
	// "container"). Empty means subprocess.
	RunMode string

	// ImageTofu / ImageTerraform are the engine-specific image fallbacks
	// when a workspace's settings.json has no Image override.
	ImageTofu      string
	ImageTerraform string
}

// New constructs a local backend with no projects yet.
func New(id, name string) *Backend {
	return &Backend{id: id, name: name}
}

// SetRuntimeDefaults snapshots the app-wide runtime preferences into the
// backend so per-run resolution doesn't need to re-load config.toml on
// every plan invocation. Call before the backend is published to UI code.
func (b *Backend) SetRuntimeDefaults(d RuntimeDefaults) {
	b.defaults = d
}

// AddProject registers a project. Not safe for concurrent use; call before
// the backend is published to other goroutines.
func (b *Backend) AddProject(p Project) {
	b.projects = append(b.projects, p)
}

func (b *Backend) ID() string                        { return b.id }
func (b *Backend) Kind() domain.BackendKind          { return domain.BackendKindLocal }
func (b *Backend) DisplayName() string               { return b.name }
func (b *Backend) Capabilities() domain.Capabilities {
	return domain.CapPlan | domain.CapApply | domain.CapVarSets | domain.CapState
}
func (b *Backend) Close() error { return nil }

// Workspaces returns one workspace per registered project, named "default".
// M2 will replace the hardcoded default with `tofu workspace list` output.
func (b *Backend) Workspaces(_ context.Context) ([]domain.Workspace, error) {
	out := make([]domain.Workspace, 0, len(b.projects))
	for _, p := range b.projects {
		out = append(out, domain.Workspace{
			ID:               b.id + ":" + p.ID + ":default",
			BackendID:        b.id,
			Name:             "default",
			ProjectName:      p.Name,
			ProjectID:        p.ID,
			WorkingDirectory: p.Path,
			ExecutionMode:    "local",
		})
	}
	return out, nil
}

// Workspace looks up a single workspace by ID. Returns domain.ErrNotFound if
// the ID isn't recognised.
func (b *Backend) Workspace(ctx context.Context, id string) (domain.Workspace, error) {
	all, err := b.Workspaces(ctx)
	if err != nil {
		return domain.Workspace{}, err
	}
	for _, w := range all {
		if w.ID == id {
			return w, nil
		}
	}
	return domain.Workspace{}, domain.ErrNotFound
}

// StartRun is the entry point for plan/apply execution against a local
// project. Wired in run.go.
func (b *Backend) StartRun(ctx context.Context, req domain.RunRequest) (domain.Run, domain.RunStream, domain.CancelFunc, error) {
	return b.startRun(ctx, req)
}

// Runs returns past runs for a workspace, oldest first. Wired through the
// optional `runListing` interface in the UI layer (see window.findRuns).
func (b *Backend) Runs(_ context.Context, workspaceID string) ([]domain.Run, error) {
	h, err := runner.NewHistory(b.id, workspaceID)
	if err != nil {
		return nil, err
	}
	entries, err := h.List()
	if err != nil {
		return nil, err
	}
	out := make([]domain.Run, 0, len(entries))
	for _, e := range entries {
		out = append(out, domain.Run{
			ID:          e.ID,
			WorkspaceID: e.WorkspaceID,
			BackendID:   e.BackendID,
			Kind:        e.Kind,
			Status:      e.Status,
			Message:     e.Message,
			CreatedAt:   e.CreatedAt,
			UpdatedAt:   e.UpdatedAt,
			PlanFile:    e.PlanFile,
			RunDir:      e.RunDir,
			ExitCode:    e.ExitCode,
		})
	}
	return out, nil
}
