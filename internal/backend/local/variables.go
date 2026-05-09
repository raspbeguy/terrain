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

// secretKey: var/<backend>/<workspace>/<name>.
func secretKey(backendID, workspaceID, name string) string {
	return "var/" + backendID + "/" + sanitize(workspaceID) + "/" + name
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(s)
}

// VariablesForWorkspace masks sensitive values; plaintext stays in the
// keyring until run-materialise time.
func (b *Backend) VariablesForWorkspace(_ context.Context, workspaceID string) ([]domain.Variable, error) {
	ws, err := b.Workspace(context.Background(), workspaceID)
	if err != nil {
		return nil, err
	}
	overrides, _ := overridesPath(b.id, workspaceID)
	hvars, err := hcl.LoadVariablesWithExtras(ws.WorkingDirectory, overrides)
	// LoadVariablesWithExtras returns partial results alongside err so
	// the UI can show what parsed even if some files didn't.
	out := make([]domain.Variable, 0, len(hvars))
	for _, v := range hvars {
		dv := domain.Variable{
			Key:         v.Name,
			Description: v.Description,
			Category:    domain.VarCategoryTerraform,
			// Declared = found in a `variable "<name>" {}` block; tfvars-
			// only entries come back with SourceFile="".
			Declared: v.SourceFile != "",
		}
		if v.Sensitive {
			dv.Sensitive = true
		}
		// Keyring entry implies sensitive even when source didn't say so.
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

// UpsertVariable routes by category: plain terraform → overrides.tfvars
// in XDG_DATA_HOME (out of the project tree); sensitive → keyring only,
// resolved at run time; env → keyring + env-vars.json index.
func (b *Backend) UpsertVariable(ctx context.Context, workspaceID string, v domain.Variable) error {
	if _, err := b.Workspace(ctx, workspaceID); err != nil {
		return err
	}

	overrides, err := overridesPath(b.id, workspaceID)
	if err != nil {
		return err
	}

	// Clear all prior storage namespaces so a category transition
	// (env↔terraform, sensitive↔plain) doesn't leave a stale value.
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

// DeleteVariable is idempotent and never touches the project's own
// terraform.tfvars (terrain doesn't write there).
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

// ctyToString renders a cty.Value as a single-line string. Complex
// types fall through to canonical HCL via hclwrite.
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
	// hclwrite serialises complex types as canonical HCL; readable
	// rather than cty.GoString's representation.
	return strings.TrimSpace(string(hclwrite.TokensForValue(v).Bytes()))
}
