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
	overrides, _ := overridesPath(b.id, workspaceID)
	hvars, err := hcl.LoadVariablesWithExtras(ws.WorkingDirectory, overrides)
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
			// SourceFile is set by the HCL loader only when the variable was
			// found in a `variable "<name>" {}` block — entries that exist
			// only in terraform.tfvars come back with SourceFile="".
			Declared: v.SourceFile != "",
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

// UpsertVariable writes a workspace variable. Storage by category:
//
//   - Plain terraform var: written to the per-workspace overrides tfvars
//     under $XDG_DATA_HOME/terrain/<backend>/<ws>/overrides.tfvars, NOT the
//     project's own terraform.tfvars. This keeps terrain-managed values out
//     of the user's source tree where they could be accidentally committed.
//   - Sensitive terraform var: keyring only. No on-disk placeholder needed
//     anymore now that overrides aren't intermingled with the project's
//     own tfvars; the run materialiser pulls the resolved value from the
//     keyring and writes it into a 0600 vars.auto.tfvars.json that lives
//     in the per-run cache dir.
//   - Env-category var: keyring (with name-only index in env-vars.json).
//     Exported into the run subprocess env, never written to any tfvars.
func (b *Backend) UpsertVariable(ctx context.Context, workspaceID string, v domain.Variable) error {
	if _, err := b.Workspace(ctx, workspaceID); err != nil {
		return err
	}

	overrides, err := overridesPath(b.id, workspaceID)
	if err != nil {
		return err
	}

	// Defensive cleanup: clear keyring entries for the OTHER namespace and
	// any prior overrides-file entry so a category transition (env↔terraform,
	// sensitive↔plain) doesn't leave a stale value behind. Idempotent — all
	// targets accept "missing" gracefully.
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

	// Plain variable: write the literal to the overrides file.
	if v.HCL {
		return hcl.UpsertTfvarFileExpr(overrides, v.Key, v.Value)
	}
	return hcl.UpsertTfvarFile(overrides, v.Key, cty.StringVal(v.Value))
}

// DeleteVariable removes a variable from terrain's overrides file and clears
// any keyring + env-index entry. Idempotent. Project's own terraform.tfvars
// is intentionally left alone — terrain doesn't write there, so it shouldn't
// delete from there either.
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
