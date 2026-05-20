package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-tfe"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
)

type tfjsonState = tfjson.State

type Flavor string

const (
	FlavorHCP Flavor = "hcp"
	FlavorTFE Flavor = "tfe"
	FlavorOTF Flavor = "otf"
)

// Endpoint defaults to app.terraform.io for HCP; required for OTF/TFE.
type Config struct {
	ID           string
	Name         string
	Flavor       Flavor
	Endpoint     string
	Organization string
	Token        string
}

type Backend struct {
	id           string
	name         string
	flavor       Flavor
	organization string

	client *tfe.Client

	capsMu sync.RWMutex
	caps   domain.Capabilities
}

func New(cfg Config) (*Backend, error) {
	if cfg.Organization == "" {
		return nil, errors.New("organization required")
	}
	token := cfg.Token
	if token == "" {
		token = os.Getenv("TFE_TOKEN")
	}
	if token == "" {
		return nil, errors.New("no API token: save in config or set TFE_TOKEN env var")
	}

	address := strings.TrimRight(cfg.Endpoint, "/")
	if address == "" {
		switch cfg.Flavor {
		case FlavorHCP:
			address = "https://app.terraform.io"
		case FlavorOTF, FlavorTFE:
			return nil, fmt.Errorf("%s backends require an explicit endpoint URL", cfg.Flavor)
		}
	}

	client, err := tfe.NewClient(&tfe.Config{
		Address: address,
		Token:   token,
	})
	if err != nil {
		return nil, fmt.Errorf("create TFE client: %w", err)
	}

	b := &Backend{
		id:           cfg.ID,
		name:         cfg.Name,
		flavor:       cfg.Flavor,
		organization: cfg.Organization,
		client:       client,
	}
	b.caps = optimisticCaps(cfg.Flavor)
	return b, nil
}

func optimisticCaps(flavor Flavor) domain.Capabilities {
	caps := domain.CapPlan | domain.CapApply | domain.CapVarSets |
		domain.CapState | domain.CapVCS | domain.CapRunQueue
	if flavor != FlavorOTF {
		caps |= domain.CapPolicy | domain.CapCostEst
	}
	return caps
}

func (b *Backend) ID() string { return b.id }

func (b *Backend) Kind() domain.BackendKind {
	switch b.flavor {
	case FlavorOTF:
		return domain.BackendKindOTF
	case FlavorHCP:
		return domain.BackendKindHCP
	case FlavorTFE:
		return domain.BackendKindTFE
	}
	return domain.BackendKindHCP
}

func (b *Backend) DisplayName() string { return b.name }

func (b *Backend) Capabilities() domain.Capabilities {
	b.capsMu.RLock()
	defer b.capsMu.RUnlock()
	return b.caps
}

func (b *Backend) Workspaces(ctx context.Context) ([]domain.Workspace, error) {
	var out []domain.Workspace
	for item := range b.StreamWorkspaces(ctx) {
		if item.Err != nil {
			return nil, item.Err
		}
		out = append(out, item.Workspaces...)
	}
	return out, nil
}

// One channel item per API page; project name piggy-backed via WSProject.
func (b *Backend) StreamWorkspaces(ctx context.Context) <-chan domain.WorkspaceStreamItem {
	out := make(chan domain.WorkspaceStreamItem, 4)
	go func() {
		defer close(out)
		page := 1
		for {
			opts := &tfe.WorkspaceListOptions{
				ListOptions: tfe.ListOptions{PageNumber: page, PageSize: 100},
				Include:     []tfe.WSIncludeOpt{tfe.WSProject},
			}
			list, err := b.client.Workspaces.List(ctx, b.organization, opts)
			if err != nil {
				select {
				case out <- domain.WorkspaceStreamItem{Err: fmt.Errorf("list workspaces (page %d): %w", page, err)}:
				case <-ctx.Done():
				}
				return
			}
			pageWS := make([]domain.Workspace, 0, len(list.Items))
			for _, ws := range list.Items {
				pageWS = append(pageWS, b.toWorkspace(ws))
			}
			select {
			case out <- domain.WorkspaceStreamItem{Workspaces: pageWS}:
			case <-ctx.Done():
				return
			}
			if list.CurrentPage >= list.TotalPages || list.TotalPages == 0 {
				return
			}
			page = list.NextPage
		}
	}()
	return out
}

func (b *Backend) Workspace(ctx context.Context, id string) (domain.Workspace, error) {
	ws, err := b.client.Workspaces.ReadByIDWithOptions(ctx, id, &tfe.WorkspaceReadOptions{
		Include: []tfe.WSIncludeOpt{tfe.WSProject},
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("read workspace %s: %w", id, err)
	}
	return b.toWorkspace(ws), nil
}

// Oldest-first, matching the local backend's history shape.
func (b *Backend) Runs(ctx context.Context, workspaceID string) ([]domain.Run, error) {
	var out []domain.Run
	page := 1
	for {
		opts := &tfe.RunListOptions{
			ListOptions: tfe.ListOptions{PageNumber: page, PageSize: 50},
		}
		list, err := b.client.Runs.List(ctx, workspaceID, opts)
		if err != nil {
			return nil, fmt.Errorf("list runs (page %d): %w", page, err)
		}
		for _, r := range list.Items {
			out = append(out, b.toRun(r, workspaceID))
		}
		if list.CurrentPage >= list.TotalPages || list.TotalPages == 0 {
			break
		}
		page = list.NextPage
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (b *Backend) toRun(r *tfe.Run, workspaceID string) domain.Run {
	status, _ := mapStatus(r.Status)
	kind := domain.RunKindPlan
	if r.IsDestroy {
		kind = domain.RunKindDestroy
	}
	return domain.Run{
		ID:          r.ID,
		WorkspaceID: workspaceID,
		BackendID:   b.id,
		Kind:        kind,
		Status:      status,
		Message:     r.Message,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.CreatedAt,
	}
}

// Prefers JSON download (Terraform 1.3+) with fallback to the binary state.
func (b *Backend) LoadState(parent context.Context, workspaceID string) (*tfjsonState, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	sv, err := b.client.StateVersions.ReadCurrent(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, tfe.ErrResourceNotFound) {
			return nil, fmt.Errorf("workspace has no state yet")
		}
		return nil, fmt.Errorf("read current state version: %w", err)
	}

	url := sv.JSONDownloadURL
	if url == "" {
		url = sv.DownloadURL
	}
	if url == "" {
		return nil, errors.New("state version has no download URL")
	}

	body, err := b.client.StateVersions.Download(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("download state: %w", err)
	}

	var state tfjsonState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("decode state json: %w", err)
	}
	return &state, nil
}

func (b *Backend) Close() error { return nil }

func (b *Backend) toWorkspace(ws *tfe.Workspace) domain.Workspace {
	projectName := b.organization
	var projectID string
	if ws.Project != nil && ws.Project.Name != "" {
		projectName = ws.Project.Name
		projectID = ws.Project.ID
	}
	return domain.Workspace{
		ID:               ws.ID,
		BackendID:        b.id,
		Name:             ws.Name,
		ProjectName:      projectName,
		ProjectID:        projectID,
		WorkingDirectory: ws.WorkingDirectory,
		TerraformVersion: ws.TerraformVersion,
		ExecutionMode:    ws.ExecutionMode,
		Description:      ws.Description,
		Locked:           ws.Locked,
	}
}
