package remote

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/go-tfe"

	"github.com/raspbeguy/terrain/internal/domain"
)

// VariablesForWorkspace lists the variables of a remote workspace, mapping
// TFE's category/HCL/sensitive into the same domain.Variable shape the local
// backend uses. Sensitive values come back from TFE as empty strings (the
// API never returns them); that aligns with our UI which masks them.
func (b *Backend) VariablesForWorkspace(parent context.Context, workspaceID string) ([]domain.Variable, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	list, err := b.client.Variables.ListAll(ctx, workspaceID, nil)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	out := make([]domain.Variable, 0, len(list.Items))
	for _, v := range list.Items {
		out = append(out, fromTFE(v))
	}
	return out, nil
}

// UpsertVariable creates or updates a workspace variable via the TFE API.
// We list-then-pick to find an existing entry by key (TFE's update path is
// keyed by variable ID, not name); the network cost is one extra round-trip
// per save which is acceptable for a low-frequency operation.
func (b *Backend) UpsertVariable(parent context.Context, workspaceID string, v domain.Variable) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	existing, err := b.findVariableByKey(ctx, workspaceID, v.Key)
	if err != nil && !errors.Is(err, tfe.ErrResourceNotFound) {
		return fmt.Errorf("lookup existing: %w", err)
	}

	cat := tfe.CategoryTerraform
	if v.Category == domain.VarCategoryEnvironment {
		cat = tfe.CategoryEnv
	}

	if existing == nil {
		_, err := b.client.Variables.Create(ctx, workspaceID, tfe.VariableCreateOptions{
			Key:         tfe.String(v.Key),
			Value:       tfe.String(v.Value),
			Description: tfe.String(v.Description),
			Category:    &cat,
			HCL:         tfe.Bool(v.HCL),
			Sensitive:   tfe.Bool(v.Sensitive),
		})
		if err != nil {
			return fmt.Errorf("create variable: %w", err)
		}
		return nil
	}

	opts := tfe.VariableUpdateOptions{
		Key:         tfe.String(v.Key),
		Description: tfe.String(v.Description),
		Category:    &cat,
		HCL:         tfe.Bool(v.HCL),
		Sensitive:   tfe.Bool(v.Sensitive),
	}
	// Only forward Value when the user actually entered something; this
	// preserves the existing TFE-side value when they edit metadata of a
	// sensitive variable without retyping the secret.
	if v.Value != "" || !v.Sensitive {
		opts.Value = tfe.String(v.Value)
	}
	if _, err := b.client.Variables.Update(ctx, workspaceID, existing.ID, opts); err != nil {
		return fmt.Errorf("update variable: %w", err)
	}
	return nil
}

// DeleteVariable removes the named variable from the workspace. Missing
// variables are not errors (matches the local backend's idempotent shape).
func (b *Backend) DeleteVariable(parent context.Context, workspaceID, key string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	existing, err := b.findVariableByKey(ctx, workspaceID, key)
	if err != nil {
		if errors.Is(err, tfe.ErrResourceNotFound) {
			return nil
		}
		return fmt.Errorf("lookup variable: %w", err)
	}
	if existing == nil {
		return nil
	}
	if err := b.client.Variables.Delete(ctx, workspaceID, existing.ID); err != nil {
		return fmt.Errorf("delete variable: %w", err)
	}
	return nil
}

func (b *Backend) findVariableByKey(ctx context.Context, workspaceID, key string) (*tfe.Variable, error) {
	list, err := b.client.Variables.ListAll(ctx, workspaceID, nil)
	if err != nil {
		return nil, err
	}
	for _, v := range list.Items {
		if v.Key == key {
			return v, nil
		}
	}
	return nil, nil
}

func fromTFE(v *tfe.Variable) domain.Variable {
	cat := domain.VarCategoryTerraform
	if v.Category == tfe.CategoryEnv {
		cat = domain.VarCategoryEnvironment
	}
	return domain.Variable{
		Key:         v.Key,
		Value:       v.Value,
		Description: v.Description,
		Category:    cat,
		HCL:         v.HCL,
		Sensitive:   v.Sensitive,
	}
}
