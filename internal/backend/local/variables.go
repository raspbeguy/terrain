package local

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/hcl"
	"github.com/raspbeguy/terrain/internal/secrets"
)

func secretKey(backendID, workspaceID, name string) string {
	return "var/" + backendID + "/" + sanitize(workspaceID) + "/" + name
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(s)
}

// Sensitive values masked; plaintext stays in the keyring until materialize.
func (b *Backend) VariablesForWorkspace(_ context.Context, workspaceID string) ([]domain.Variable, error) {
	ws, err := b.Workspace(context.Background(), workspaceID)
	if err != nil {
		return nil, err
	}
	overrides, _ := overridesPath(b.id, workspaceID)
	hvars, err := hcl.LoadVariablesWithExtras(ws.WorkingDirectory, overrides)
	out := make([]domain.Variable, 0, len(hvars))
	for _, v := range hvars {
		dv := domain.Variable{
			Key:         v.Name,
			Description: v.Description,
			Category:    domain.VarCategoryTerraform,
			Declared:    v.SourceFile != "",
		}
		if v.Sensitive {
			dv.Sensitive = true
		}
		if _, kerr := secrets.Get(secretKey(b.id, workspaceID, v.Name)); kerr == nil {
			dv.Sensitive = true
		}
		switch {
		case dv.Sensitive:
			dv.Value = ""
		case v.Override != nil:
			dv.Value = ctyToString(*v.Override)
		case v.Default != nil:
			dv.Value = ctyToString(*v.Default)
		}
		out = append(out, dv)
	}
	return out, err
}

// Routes by category: plain → overrides.tfvars; sensitive → keyring; env → keyring + index.
func (b *Backend) UpsertVariable(ctx context.Context, workspaceID string, v domain.Variable) error {
	if _, err := b.Workspace(ctx, workspaceID); err != nil {
		return err
	}

	overrides, err := overridesPath(b.id, workspaceID)
	if err != nil {
		return err
	}

	// Clear all namespaces so category transitions don't leave stale values.
	_ = secrets.Delete(secretKey(b.id, workspaceID, v.Key))
	_ = secrets.Delete(envKey(b.id, workspaceID, v.Key))
	_ = removeEnvVar(b.id, workspaceID, v.Key)
	_ = hcl.DeleteTfvarFile(overrides, v.Key)

	if v.Category == domain.VarCategoryEnvironment {
		if err := secrets.Set(envKey(b.id, workspaceID, v.Key), v.Value); err != nil {
			return err
		}
		return addEnvVar(b.id, workspaceID, v.Key)
	}

	if v.Sensitive {
		if err := secrets.Set(secretKey(b.id, workspaceID, v.Key), v.Value); err != nil {
			return fmt.Errorf("store sensitive value: %w", err)
		}
		return nil
	}

	if v.HCL {
		return hcl.UpsertTfvarFileExpr(overrides, v.Key, v.Value)
	}
	return hcl.UpsertTfvarFile(overrides, v.Key, cty.StringVal(v.Value))
}

// Idempotent; never touches the project's own terraform.tfvars.
func (b *Backend) DeleteVariable(ctx context.Context, workspaceID, key string) error {
	if _, err := b.Workspace(ctx, workspaceID); err != nil {
		return err
	}
	overrides, err := overridesPath(b.id, workspaceID)
	if err != nil {
		return err
	}
	_ = secrets.Delete(secretKey(b.id, workspaceID, key))
	_ = secrets.Delete(envKey(b.id, workspaceID, key))
	_ = removeEnvVar(b.id, workspaceID, key)
	return hcl.DeleteTfvarFile(overrides, key)
}

func envKey(backendID, workspaceID, name string) string {
	return "env/" + backendID + "/" + sanitize(workspaceID) + "/" + name
}

func ctyToString(v cty.Value) string {
	if !v.IsKnown() || v.IsNull() {
		return ""
	}
	switch v.Type() {
	case cty.String:
		return v.AsString()
	case cty.Bool:
		if v.True() {
			return "true"
		}
		return "false"
	case cty.Number:
		return v.AsBigFloat().Text('g', 12)
	}
	return strings.TrimSpace(string(hclwrite.TokensForValue(v).Bytes()))
}
