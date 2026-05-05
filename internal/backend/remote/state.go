package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/go-tfe"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
)

// StateVersions: TFE's List filters by workspace name, not ID, so we
// resolve via ReadByID first.
func (b *Backend) StateVersions(parent context.Context, workspaceID string) ([]domain.StateVersion, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	ws, err := b.client.Workspaces.ReadByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read workspace %s: %w", workspaceID, err)
	}

	var out []domain.StateVersion
	page := 1
	for {
		list, err := b.client.StateVersions.List(ctx, &tfe.StateVersionListOptions{
			ListOptions:  tfe.ListOptions{PageNumber: page, PageSize: 50},
			Organization: b.organization,
			Workspace:    ws.Name,
		})
		if err != nil {
			return nil, fmt.Errorf("list state versions (page %d): %w", page, err)
		}
		for _, sv := range list.Items {
			out = append(out, b.toStateVersion(sv, workspaceID))
		}
		if list.CurrentPage >= list.TotalPages || list.TotalPages == 0 {
			break
		}
		page = list.NextPage
	}
	return out, nil
}

// LoadStateVersion prefers the JSON URL (TF 1.3+); falls back to the raw .tfstate URL.
func (b *Backend) LoadStateVersion(parent context.Context, _ string, versionID string) (*tfjson.State, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	sv, err := b.client.StateVersions.Read(ctx, versionID)
	if err != nil {
		if errors.Is(err, tfe.ErrResourceNotFound) {
			return nil, fmt.Errorf("state version %s not found", versionID)
		}
		return nil, fmt.Errorf("read state version: %w", err)
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

	var state tfjson.State
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("decode state json: %w", err)
	}
	return &state, nil
}

func (b *Backend) toStateVersion(sv *tfe.StateVersion, workspaceID string) domain.StateVersion {
	return domain.StateVersion{
		ID:          sv.ID,
		BackendID:   b.id,
		WorkspaceID: workspaceID,
		Serial:      int64(sv.Serial),
		CreatedAt:   sv.CreatedAt,
	}
}
