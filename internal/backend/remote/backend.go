// Package remote implements domain.Backend against the Terraform Enterprise
// API surface (HCP Terraform, self-hosted TFE, and OTF). All three flavors
// share the same JSON-API contract, so we drive them through a single
// hashicorp/go-tfe client and differentiate on capability probing.
//
// M4 ships listing only — StartRun returns ErrNotImplemented because remote
// run execution requires a polling/SSE bridge that's substantial enough to
// warrant its own session. State and run history reads will follow the same
// pattern as listing once StartRun lands.
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

// tfjsonState aliases to keep the LoadState signature short and consistent
// with terraform-json's own naming.
type tfjsonState = tfjson.State

// Flavor names the sub-family of TFE-API-compatible backend.
type Flavor string

const (
	FlavorHCP Flavor = "hcp" // app.terraform.io
	FlavorTFE Flavor = "tfe" // self-hosted Terraform Enterprise
	FlavorOTF Flavor = "otf" // leg100/otf
)

// Config is what callers pass to New. Endpoint may be empty for HCP (we
// default to https://app.terraform.io); OTF and TFE require an explicit URL.
type Config struct {
	ID           string
	Name         string
	Flavor       Flavor
	Endpoint     string
	Organization string
	// Token may be empty; New falls back to TFE_TOKEN env var.
	Token string
}

// Backend is the live remote-backend implementation.
type Backend struct {
	id           string
	name         string
	flavor       Flavor
	organization string

	client *tfe.Client

	// Capabilities are seeded optimistically per flavor at New() and
	// refined by Probe() (called as part of TestConnection). Reads after
	// probe see the refined value.
	capsMu sync.RWMutex
	caps   domain.Capabilities
}

// New constructs a Backend from cfg. Returns errors for missing organization
// or unresolvable token; the caller surfaces these to the user (typically
// via the Add Remote Backend dialog's "Test Connection" button).
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

// optimisticCaps returns the assumed capability bitmask for a flavor before
// any probing — what we display when the network is slow or the user hasn't
// hit Test Connection yet. Probe() refines this from the actual API.
func optimisticCaps(flavor Flavor) domain.Capabilities {
	caps := domain.CapPlan | domain.CapApply | domain.CapVarSets |
		domain.CapState | domain.CapVCS | domain.CapRunQueue
	if flavor != FlavorOTF {
		caps |= domain.CapPolicy | domain.CapCostEst
	}
	return caps
}

// ID returns the registry-stable identifier.
func (b *Backend) ID() string { return b.id }

// Kind maps internal flavor → domain.BackendKind.
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

// DisplayName is the user-facing label shown in the sidebar group header.
func (b *Backend) DisplayName() string { return b.name }

// Capabilities returns the cached bitmask. Initial value comes from
// optimisticCaps(flavor); Probe() refines it once the API has been hit.
// Reads are guarded by a RWMutex so concurrent UI queries don't race
// against an in-flight probe.
func (b *Backend) Capabilities() domain.Capabilities {
	b.capsMu.RLock()
	defer b.capsMu.RUnlock()
	return b.caps
}

// Workspaces lists every workspace in the configured organization. Pages are
// flattened into one slice; M4's typical organizations have low-thousands of
// workspaces — we'll add lazy pagination if that becomes an issue.
func (b *Backend) Workspaces(ctx context.Context) ([]domain.Workspace, error) {
	var out []domain.Workspace
	page := 1
	for {
		opts := &tfe.WorkspaceListOptions{
			ListOptions: tfe.ListOptions{PageNumber: page, PageSize: 100},
			// Pull the project relation in the same request so the sidebar
			// can show the workspace's project name without a per-workspace
			// follow-up read. OTF supports the `project` include since
			// v0.3.x; older deployments will return workspaces with a nil
			// Project relation, and we fall back to the org name then.
			Include: []tfe.WSIncludeOpt{tfe.WSProject},
		}
		list, err := b.client.Workspaces.List(ctx, b.organization, opts)
		if err != nil {
			return nil, fmt.Errorf("list workspaces (page %d): %w", page, err)
		}
		for _, ws := range list.Items {
			out = append(out, b.toWorkspace(ws))
		}
		if list.CurrentPage >= list.TotalPages || list.TotalPages == 0 {
			break
		}
		page = list.NextPage
	}
	return out, nil
}

// Workspace looks up a single workspace by ID. Uses the WithOptions variant
// so the project relation is included — same rationale as the list call.
func (b *Backend) Workspace(ctx context.Context, id string) (domain.Workspace, error) {
	ws, err := b.client.Workspaces.ReadByIDWithOptions(ctx, id, &tfe.WorkspaceReadOptions{
		Include: []tfe.WSIncludeOpt{tfe.WSProject},
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("read workspace %s: %w", id, err)
	}
	return b.toWorkspace(ws), nil
}

// StartRun is implemented in run.go.

// Runs returns past runs for one workspace, oldest first. Wired through the
// runListing interface in the UI; the local backend implements the same
// contract from the on-disk ndjson history.
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
	// Reverse to oldest-first to match the local backend's history shape.
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
		UpdatedAt:   r.CreatedAt, // TFE doesn't expose updated-at on the list payload
	}
}

// LoadState fetches the workspace's current state version, prefers the
// JSON download URL (Terraform 1.3+) and falls back to the binary state
// download. Returns *tfjson.State so the State tab widget renders unchanged.
//
// Implements the same `stateLoader` interface the local backend type-asserts
// against — see internal/ui/window/window.go.
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

// Close is a no-op — *tfe.Client uses pooled net/http and has nothing to
// release.
func (b *Backend) Close() error { return nil }

func (b *Backend) toWorkspace(ws *tfe.Workspace) domain.Workspace {
	// Prefer the workspace's TFE/OTF project name (e.g. "infra-prod"); fall
	// back to the org name when the API didn't surface a project relation —
	// older OTF versions, or workspaces left in the org's default project
	// without an explicit project assignment.
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
