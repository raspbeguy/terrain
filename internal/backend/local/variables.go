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

// secretSentinel is the literal placeholder we write into terraform.tfvars
// for sensitive variables. The runner detects it at run-materialisation time
// and resolves the real value from the keyring.
//
// Format note: we write a string `null` so terraform parses it as null
// (effectively absent), with a trailing comment carrying the marker. The
// hclwrite serializer doesn't preserve trailing comments on simple values
// reliably across versions, so we also key the keyring lookup off the
// variable name itself — the comment is informational, not load-bearing.
const secretSentinel = "@terrain:secret"

// secretKey is the keyring key under which a sensitive workspace variable
// is stored. Format: var/<backend>/<workspace>/<name>.
func secretKey(backendID, workspaceID, name string) string {
	return "var/" + backendID + "/" + sanitize(workspaceID) + "/" + name
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(s)
}

// VariablesForWorkspace returns the discovered variables of a workspace,
// merged with any current overrides in terraform.tfvars. Sensitive values
// are masked here — the actual plaintext stays in the keyring until a run
// materialises.
//
// This wraps internal/hcl.LoadVariables and adapts to domain.Variable.
func (b *Backend) VariablesForWorkspace(_ context.Context, workspaceID string) ([]domain.Variable, error) {
	ws, err := b.Workspace(context.Background(), workspaceID)
	if err != nil {
		return nil, err
	}
	hvars, err := hcl.LoadVariables(ws.WorkingDirectory)
	if err != nil {
		// Diagnostics aren't fatal — the loader returns whatever it could
		// parse alongside the error. We propagate the error so the UI can
		// surface a banner, but still bind whatever we got.
	}
	out := make([]domain.Variable, 0, len(hvars))
	for _, v := range hvars {
		dv := domain.Variable{
			Key:         v.Name,
			Description: v.Description,
			Category:    domain.VarCategoryTerraform,
		}
		// Best-effort sensitivity flag: the .tfvars value being our sentinel
		// or a corresponding keyring entry existing both indicate sensitive.
		if v.Sensitive {
			dv.Sensitive = true
		}
		if _, kerr := secrets.Get(secretKey(b.id, workspaceID, v.Name)); kerr == nil {
			dv.Sensitive = true
		}
		switch {
		case dv.Sensitive:
			dv.Value = "" // never expose
		case v.Override != nil:
			dv.Value = ctyToString(*v.Override)
		case v.Default != nil:
			dv.Value = ctyToString(*v.Default)
		}
		out = append(out, dv)
	}
	return out, err
}

// UpsertVariable writes a workspace variable. For sensitive vars it stores
// the value in the keyring and writes a `null` placeholder; otherwise it
// writes directly to terraform.tfvars via hclwrite (round-trip preserves
// comments and other attributes).
//
// Category=env vars are stored in the keyring keyed by env/<key> regardless
// of sensitive flag — they're exported into the run subprocess at
// materialisation time, never written to .tfvars.
func (b *Backend) UpsertVariable(ctx context.Context, workspaceID string, v domain.Variable) error {
	ws, err := b.Workspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	// Defensive cleanup: clear keyring entries for the OTHER namespace so
	// a category transition (env↔terraform, sensitive↔plain) doesn't leave
	// a stale value behind. Idempotent — Delete on a missing key is a no-op.
	_ = secrets.Delete(secretKey(b.id, workspaceID, v.Key))
	_ = secrets.Delete(envKey(b.id, workspaceID, v.Key))
	_ = removeEnvVar(ws.WorkingDirectory, workspaceID, v.Key)

	if v.Category == domain.VarCategoryEnvironment {
		// Env vars always go through keyring; runner injects them later.
		if err := secrets.Set(envKey(b.id, workspaceID, v.Key), v.Value); err != nil {
			return err
		}
		return addEnvVar(ws.WorkingDirectory, workspaceID, v.Key)
	}

	if v.Sensitive {
		if err := secrets.Set(secretKey(b.id, workspaceID, v.Key), v.Value); err != nil {
			return fmt.Errorf("store sensitive value: %w", err)
		}
		// Write a placeholder so the variable is "set" from terraform's
		// perspective (the runner replaces it).
		return hcl.UpsertTfvar(ws.WorkingDirectory, v.Key, cty.NullVal(cty.String))
	}

	// Plain variable: write the literal to .tfvars.

	if v.HCL {
		return hcl.UpsertTfvarExpr(ws.WorkingDirectory, v.Key, v.Value)
	}
	return hcl.UpsertTfvar(ws.WorkingDirectory, v.Key, cty.StringVal(v.Value))
}

// DeleteVariable removes a variable from the workspace's tfvars and clears
// any keyring + env-index entry. Idempotent.
func (b *Backend) DeleteVariable(ctx context.Context, workspaceID, key string) error {
	ws, err := b.Workspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	_ = secrets.Delete(secretKey(b.id, workspaceID, key))
	_ = secrets.Delete(envKey(b.id, workspaceID, key))
	_ = removeEnvVar(ws.WorkingDirectory, workspaceID, key)
	return hcl.DeleteTfvar(ws.WorkingDirectory, key)
}

func envKey(backendID, workspaceID, name string) string {
	return "env/" + backendID + "/" + sanitize(workspaceID) + "/" + name
}

// ctyToString collapses a cty.Value to its string display. Mirrors the UI
// helper but lives here so backend-side rendering doesn't depend on the
// widgets package. Kept best-effort: complex types are JSON-encoded.
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
	// Complex types (objects, maps, tuples, lists): serialize as canonical
	// HCL via hclwrite.TokensForValue, then trim. The result reads like the
	// user wrote it — `{"a" = "b"}` rather than `cty.ObjectVal(...)`.
	return strings.TrimSpace(string(hclwrite.TokensForValue(v).Bytes()))
}
