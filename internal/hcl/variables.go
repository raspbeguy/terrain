// Package hcl: hashicorp/hcl/v2 helpers for .tf and .tfvars variables.
package hcl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

type Variable struct {
	Name        string
	Description string
	Type        string
	Default     *cty.Value
	Sensitive   bool
	Override    *cty.Value
	SourceFile  string
}

// Later extraPaths override earlier tfvars for the same key; missing files are silent.
func LoadVariablesWithExtras(projectDir string, extraPaths ...string) ([]Variable, error) {
	parser := hclparse.NewParser()

	tfFiles, tfvarsFiles, err := listHCLFiles(projectDir)
	if err != nil {
		return nil, err
	}
	for _, p := range extraPaths {
		if _, err := os.Stat(p); err == nil {
			tfvarsFiles = append(tfvarsFiles, p)
		}
	}

	vars := map[string]*Variable{}
	var diagText []string

	for _, path := range tfFiles {
		body, diags := parseFile(parser, path)
		if diags.HasErrors() {
			diagText = append(diagText, fmt.Sprintf("%s: %s", path, diags.Error()))
			continue
		}
		extractVariables(path, body, vars)
	}

	overrides := map[string]cty.Value{}
	for _, path := range tfvarsFiles {
		body, diags := parseFile(parser, path)
		if diags.HasErrors() {
			diagText = append(diagText, fmt.Sprintf("%s: %s", path, diags.Error()))
			continue
		}
		extractTfvars(body, overrides)
	}

	for name, val := range overrides {
		v, ok := vars[name]
		if !ok {
			val := val
			vars[name] = &Variable{Name: name, Override: &val}
			continue
		}
		val := val
		v.Override = &val
	}

	out := make([]Variable, 0, len(vars))
	for _, v := range vars {
		out = append(out, *v)
	}
	sortByName(out)

	if len(diagText) > 0 {
		return out, fmt.Errorf("HCL diagnostics:\n%s", strings.Join(diagText, "\n"))
	}
	return out, nil
}

func listHCLFiles(dir string) (tf []string, tfvars []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		full := filepath.Join(dir, name)
		switch {
		case strings.HasSuffix(name, ".tf") && !strings.HasSuffix(name, ".tf.json"):
			tf = append(tf, full)
		case name == "terraform.tfvars":
			tfvars = append(tfvars, full)
		case strings.HasSuffix(name, ".auto.tfvars"):
			tfvars = append(tfvars, full)
		}
	}
	return tf, tfvars, nil
}

func parseFile(parser *hclparse.Parser, path string) (*hclsyntax.Body, hcl.Diagnostics) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "read failed",
			Detail:   err.Error(),
		}}
	}
	file, diags := parser.ParseHCL(src, path)
	if diags.HasErrors() || file == nil {
		return nil, diags
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "unexpected body type",
			Detail:   "this parser expects native HCL, not JSON",
			Subject:  &hcl.Range{Filename: path},
		}}
	}
	return body, diags
}

func extractVariables(path string, body *hclsyntax.Body, vars map[string]*Variable) {
	for _, b := range body.Blocks {
		if b.Type != "variable" || len(b.Labels) != 1 {
			continue
		}
		name := b.Labels[0]
		v := &Variable{Name: name, SourceFile: path}

		for attrName, attr := range b.Body.Attributes {
			val, _ := attr.Expr.Value(nil)
			switch attrName {
			case "description":
				if val.Type() == cty.String {
					v.Description = val.AsString()
				}
			case "type":
				v.Type = sourceText(attr.Expr.Range())
			case "default":
				v2 := val
				v.Default = &v2
			case "sensitive":
				if val.Type() == cty.Bool {
					v.Sensitive = val.True()
				}
			}
		}
		vars[name] = v
	}
}

func extractTfvars(body *hclsyntax.Body, out map[string]cty.Value) {
	for name, attr := range body.Attributes {
		val, _ := attr.Expr.Value(nil)
		out[name] = val
	}
}

func sourceText(_ hcl.Range) string {
	return ""
}

func sortByName(vs []Variable) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && vs[j-1].Name > vs[j].Name; j-- {
			vs[j-1], vs[j] = vs[j], vs[j-1]
		}
	}
}
