package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/hcl"
	"github.com/raspbeguy/terrain/internal/secrets"
)

// resolvedVars carries one run's materialized variables. Terraform-category
// values go to a -var-file the runner passes to tofu/terraform; Env-category
// values go to cmd.Env. We split them at this layer because the two have
// completely different injection paths and shouldn't get conflated.
type resolvedVars struct {
	Terraform map[string]termValue
	Env       map[string]string
}

// termValue is one terraform-category variable's resolved value, carrying
// just enough type information to serialize it correctly in the var-file.
//
// Three shapes:
//   - cty != nil: a fully-typed value parsed from the workspace's tfvars
//     (declared overrides). Emit via hclwrite.SetAttributeValue, which
//     produces type-correct HCL — number stays number, list stays list, etc.
//   - hcl=true: raw is an HCL expression (e.g. `[1, 2]` or `{"k" = "v"}`)
//     to be emitted verbatim. Used for varset entries the user marked
//     HCL=true; treated as opaque tokens by hclwrite.
//   - otherwise: a plain string. Quoted as cty.StringVal at write time.
//     Terraform auto-coerces strings to declared scalar types (number,
//     bool); for complex types the user must use HCL=true or terrain's
//     declared-override path.
//
// Why this matters: emitting "5" for a variable declared as `number` is
// fine (terraform coerces), but emitting `"[\"a\", \"b\"]"` for `list(string)`
// fails with "Invalid value for input variable". HCL output with proper
// types sidesteps the entire conversion-rules surface.
type termValue struct {
	cty *cty.Value
	raw string
	hcl bool
}

// materialize resolves run-time variable values from variable sets, declared
// workspace variables, and the keyring. Mirrors what TFE does server-side
// for remote runs:
//
// Precedence (lowest → highest, last write wins for the same key):
//
//  1. Global variable sets (sensitive values resolved from keyring)
//  2. Workspace `terraform.tfvars` plain values
//  3. Workspace sensitive variables (resolved from keyring)
//
// Writing the merged result to a `vars.auto.tfvars.json` plays nicely with
// terraform's own load order (terraform.tfvars → *.auto.tfvars.json):
// because we include the workspace plain values in our merged output too,
// terraform sees the same value from both sources for any conflicting key,
// and varset-only keys come through cleanly via auto.tfvars.json.
//
// Environment-category variables go to a separate channel: the runner appends
// them to cmd.Env. Workspace env vars override varset env vars; both are
// looked up in the keyring (varsets in their own namespace).
func (b *Backend) materialize(ws domain.Workspace) *resolvedVars {
	rv := &resolvedVars{
		Terraform: map[string]termValue{},
		Env:       map[string]string{},
	}

	// 1. Variable sets — lowest precedence. Applied in this order, with
	// later wins:
	//    a. Global sets (sorted by priority asc; higher priority wins
	//       within the same scope, but stays below project + workspace)
	//    b. Project sets matching ws.ProjectID
	//    c. Workspace sets where ws.ID is in set.Workspaces
	if sets, err := b.VariableSets(context.Background()); err == nil {
		for _, set := range applicableSets(sets, ws) {
			for _, v := range set.Variables {
				switch {
				case v.Category == domain.VarCategoryEnvironment:
					if val, err := secrets.Get(varsetEnvKey(set.ID, v.Key)); err == nil {
						rv.Env[v.Key] = val
					}
				case v.Sensitive:
					if val, err := secrets.Get(varsetSecretKey(set.ID, v.Key)); err == nil {
						rv.Terraform[v.Key] = termValue{raw: val, hcl: v.HCL}
					}
				default:
					rv.Terraform[v.Key] = termValue{raw: v.Value, hcl: v.HCL}
				}
			}
		}
	}

	if ws.WorkingDirectory == "" {
		return rv
	}

	// 2. Workspace declared variables — overlay project tfvars values, then
	// terrain's overrides file (lives outside the project tree). LoadVariables
	// merges both, with extras taking precedence over project tfvars. The
	// cty.Value carries proper type info, so hclwrite emits a typed literal
	// (number / list / object / etc.) and terraform doesn't need to coerce.
	overrides, _ := overridesPath(b.id, ws.ID)
	declared, _ := hcl.LoadVariablesWithExtras(ws.WorkingDirectory, overrides)
	for _, v := range declared {
		if v.Override != nil && !v.Override.IsNull() && (*v.Override).Type() != cty.NilType {
			cp := *v.Override
			rv.Terraform[v.Name] = termValue{cty: &cp}
		}
	}

	// 3. Workspace sensitive variables — keyring overlays last (highest).
	// Stored as raw strings; terraform auto-coerces to declared scalar types.
	// Sensitive complex-typed values are uncommon and would need to be set
	// via HCL=true through a varset to round-trip cleanly.
	for _, v := range declared {
		if val, err := secrets.Get(secretKey(b.id, ws.ID, v.Name)); err == nil {
			rv.Terraform[v.Name] = termValue{raw: val}
		}
	}

	// 4. Workspace env-category vars — overlay any varset env values.
	names, _ := loadEnvIndex(b.id, ws.ID)
	for _, name := range names {
		if val, err := secrets.Get(envKey(b.id, ws.ID, name)); err == nil {
			rv.Env[name] = val
		}
	}
	return rv
}

// writeVarFile serialises Terraform vars to an HCL tfvars file readable by
// `terraform plan -var-file=<path>`. Returns the empty string when there
// are no vars to write (caller skips adding -var-file in that case).
//
// HCL output (vs JSON) preserves type information: numbers stay numbers,
// lists stay lists, objects stay objects. Earlier we used JSON with all
// values as strings, which fell over for variables declared as anything
// non-scalar — terraform's auto-coercion only handles string→number/bool,
// not string→list/object.
//
// The file is written 0600 so a curious sibling user can't read sensitive
// values out of $XDG_CACHE_HOME/terrain/. Callers must delete the file once
// the run reaches a terminal state.
func (rv *resolvedVars) writeVarFile(runDir string) (string, error) {
	if len(rv.Terraform) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", runDir, err)
	}

	f := hclwrite.NewEmptyFile()
	body := f.Body()

	// Sort keys so the on-disk file is deterministic (helps diffing run
	// artifacts during debugging; doesn't affect terraform's behaviour).
	keys := make([]string, 0, len(rv.Terraform))
	for k := range rv.Terraform {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := rv.Terraform[k]
		switch {
		case v.cty != nil:
			body.SetAttributeValue(k, *v.cty)
		case v.hcl:
			body.SetAttributeRaw(k, hclwrite.TokensForIdentifier(v.raw))
		default:
			body.SetAttributeValue(k, cty.StringVal(v.raw))
		}
	}

	path := filepath.Join(runDir, "vars.auto.tfvars")
	if err := os.WriteFile(path, f.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// applicableSets returns the variable sets that apply to ws, in the order
// they should overlay (lowest precedence first). Within each scope, ties
// break by priority ascending (higher wins, applied last). Global sets
// always apply; project sets apply when ws.ProjectID matches; workspace
// sets apply when ws.ID is in the set's Workspaces list.
func applicableSets(all []domain.VariableSet, ws domain.Workspace) []domain.VariableSet {
	var globals, projects, workspaces []domain.VariableSet
	for _, s := range all {
		switch s.Scope {
		case domain.ScopeGlobal:
			globals = append(globals, s)
		case domain.ScopeProject:
			if s.ProjectID == ws.ProjectID && ws.ProjectID != "" {
				projects = append(projects, s)
			}
		case domain.ScopeWorkspace:
			for _, attached := range s.Workspaces {
				if attached == ws.ID {
					workspaces = append(workspaces, s)
					break
				}
			}
		}
	}
	sortByPriorityAsc(globals)
	sortByPriorityAsc(projects)
	sortByPriorityAsc(workspaces)
	out := make([]domain.VariableSet, 0, len(globals)+len(projects)+len(workspaces))
	out = append(out, globals...)
	out = append(out, projects...)
	out = append(out, workspaces...)
	return out
}

func sortByPriorityAsc(s []domain.VariableSet) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Priority > s[j].Priority; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// envEntries renders the env vars as `KEY=VAL` strings ready for
// `(*exec.Cmd).Env = append(os.Environ(), entries...)`.
func (rv *resolvedVars) envEntries() []string {
	out := make([]string, 0, len(rv.Env))
	for k, v := range rv.Env {
		out = append(out, k+"="+v)
	}
	return out
}
