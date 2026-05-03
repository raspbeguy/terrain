// Package hcl wraps hashicorp/hcl/v2 with helpers focused on Terrain's needs:
// extracting variable declarations from .tf files and current values from
// .tfvars files. The wrapper exists so the rest of the codebase doesn't need
// to learn HCL's two-pass parse model just to do simple lookups.
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

// Variable is a parsed `variable "<name>" { ... }` block from a .tf file.
type Variable struct {
	Name        string
	Description string
	Type        string // textual, e.g. "string", "list(string)" — best-effort
	Default     *cty.Value
	Sensitive   bool

	// Override is the current value from a .tfvars file or env, if found.
	// nil if the user hasn't set anything.
	Override *cty.Value

	// SourceFile is the path of the .tf file the variable was declared in.
	SourceFile string
}

// LoadVariables walks projectDir for top-level .tf files, parses every
// `variable "<name>" {}` block, and merges in current values from
// terraform.tfvars / *.auto.tfvars. Returns variables sorted by name.
//
// Errors during parsing of one file don't abort the load; we collect what we
// can and surface aggregate diagnostics through the returned error.
//
// Recursion: only the top-level directory is scanned. Modules under
// subdirectories aren't surfaced — that's a future enhancement when we have
// a richer "module variables" UI section.
func LoadVariables(projectDir string) ([]Variable, error) {
	parser := hclparse.NewParser()

	tfFiles, tfvarsFiles, err := listHCLFiles(projectDir)
	if err != nil {
		return nil, err
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
			// Variable set in tfvars but never declared — surface it anyway
			// so the user can spot stale entries.
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

// extractVariables fills out vars from `variable "<name>" {}` blocks. Type,
// default, sensitive are best-effort; Terraform's actual type expression
// language is more flexible than what we render.
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
				if !val.IsNull() {
					v := val
					vars[name] = &Variable{}
					_ = v // populated via copy below
				}
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

// extractTfvars reads top-level `key = value` pairs from a .tfvars body.
func extractTfvars(body *hclsyntax.Body, out map[string]cty.Value) {
	for name, attr := range body.Attributes {
		val, _ := attr.Expr.Value(nil)
		out[name] = val
	}
}

func sourceText(_ hcl.Range) string {
	// Reading source bytes between two ranges requires the original source.
	// Skip for now — type display can show a placeholder until M5 polish
	// when we keep the source byte slice around.
	return ""
}

func sortByName(vs []Variable) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && vs[j-1].Name > vs[j].Name; j-- {
			vs[j-1], vs[j] = vs[j], vs[j-1]
		}
	}
}
