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

type resolvedVars struct {
	Terraform map[string]termValue
	Env       map[string]string
}

// Priority: cty (typed) > hcl (raw expression) > raw string.
type termValue struct {
	cty *cty.Value
	raw string
	hcl bool
}

// Precedence: variable sets < workspace tfvars < keyring sensitives < env.
func (b *Backend) materialize(ws domain.Workspace) *resolvedVars {
	rv := &resolvedVars{
		Terraform: map[string]termValue{},
		Env:       map[string]string{},
	}

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

	overrides, _ := overridesPath(b.id, ws.ID)
	declared, _ := hcl.LoadVariablesWithExtras(ws.WorkingDirectory, overrides)
	for _, v := range declared {
		if v.Override != nil && !v.Override.IsNull() && (*v.Override).Type() != cty.NilType {
			cp := *v.Override
			rv.Terraform[v.Name] = termValue{cty: &cp}
		}
	}

	// Complex-typed sensitives must go through a varset with HCL=true.
	for _, v := range declared {
		if val, err := secrets.Get(secretKey(b.id, ws.ID, v.Name)); err == nil {
			rv.Terraform[v.Name] = termValue{raw: val}
		}
	}

	names, _ := loadEnvIndex(b.id, ws.ID)
	for _, name := range names {
		if val, err := secrets.Get(envKey(b.id, ws.ID, name)); err == nil {
			rv.Env[name] = val
		}
	}
	return rv
}

// HCL (not JSON) preserves list/object types; caller deletes on terminal status.
func (rv *resolvedVars) writeVarFile(runDir string) (string, error) {
	if len(rv.Terraform) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", runDir, err)
	}

	f := hclwrite.NewEmptyFile()
	body := f.Body()

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

// Lowest-precedence first; within scope, higher priority applied last.
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

func (rv *resolvedVars) envEntries() []string {
	out := make([]string, 0, len(rv.Env))
	for k, v := range rv.Env {
		out = append(out, k+"="+v)
	}
	return out
}
