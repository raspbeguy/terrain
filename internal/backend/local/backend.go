// Package local implements domain.Backend by shelling out to the tofu /
// terraform CLI against a git-clone of the user's project.
package local

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/runner"
)

type Project struct {
	ID          string
	Name        string
	GitURL      string
	GitRef      string
	Subpath     string
	SSHKeyLabel string
	GitUsername string

	// dirOverride is a test escape hatch — set in package tests to point at a literal directory.
	dirOverride string
}

// WorkingDir is the on-disk path the tofu/terraform process runs in.
func (p Project) WorkingDir() (string, error) {
	if p.dirOverride != "" {
		return p.dirOverride, nil
	}
	cloneDir, err := CloneDir(p.GitURL, p.GitRef)
	if err != nil {
		return "", err
	}
	return filepath.Join(cloneDir, p.Subpath), nil
}

// Backend groups all local projects under one sidebar header: one Backend, many workspaces.
type Backend struct {
	id       string
	name     string
	projects []Project
	defaults RuntimeDefaults

	wsMu    sync.RWMutex
	wsCache map[string][]string
}

type RuntimeDefaults struct {
	Engine         string // "tofu" or "terraform"
	RuntimePath    string
	RunMode        string
	ImageTofu      string
	ImageTerraform string
}

func New(id, name string) *Backend {
	return &Backend{id: id, name: name}
}

// binaryResolver: shared singleton in managed mode so UI installs and runs see one lock.
func (b *Backend) binaryResolver(s WorkspaceSettings) BinaryResolver {
	if s.BinarySource == BinarySourceManaged {
		return sharedManagedResolver()
	}
	return pathResolver{}
}

// SetRuntimeDefaults must be called before the backend is published.
func (b *Backend) SetRuntimeDefaults(d RuntimeDefaults) {
	b.defaults = d
}

// AddProject is not safe for concurrent use; call before publishing the backend.
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

func (b *Backend) Workspaces(_ context.Context) ([]domain.Workspace, error) {
	out := make([]domain.Workspace, 0, len(b.projects))
	for _, p := range b.projects {
		dir, err := p.WorkingDir()
		if err != nil {
			return nil, err
		}
		for _, name := range b.workspaceCache(p.ID) {
			out = append(out, domain.Workspace{
				ID:               b.id + ":" + p.ID + ":" + name,
				BackendID:        b.id,
				Name:             name,
				ProjectName:      p.Name,
				ProjectID:        p.ID,
				WorkingDirectory: dir,
				GitURL:           p.GitURL,
				GitRef:           p.GitRef,
				Subpath:          p.Subpath,
				ExecutionMode:    "local",
			})
		}
	}
	return out, nil
}

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

func (b *Backend) StartRun(ctx context.Context, req domain.RunRequest) (domain.Run, domain.RunStream, domain.CancelFunc, error) {
	return b.startRun(ctx, req)
}

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
