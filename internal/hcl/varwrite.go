package hcl

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// UpsertTfvar writes or replaces a single key=value entry in
// <projectDir>/terraform.tfvars. Preserves comments and ordering of unrelated
// attributes (hclwrite round-trip). When the file doesn't exist it's created
// with just this attribute.
//
// value is treated as a literal: scalar Go types (string/int/bool) become
// the obvious cty value; for HCL expression values (lists, objects, raw HCL)
// callers should use UpsertTfvarExpr.
func UpsertTfvar(projectDir, key string, value cty.Value) error {
	path := filepath.Join(projectDir, "terraform.tfvars")
	src, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var f *hclwrite.File
	if len(src) == 0 {
		f = hclwrite.NewEmptyFile()
	} else {
		var diags hcl.Diagnostics
		f, diags = hclwrite.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			return fmt.Errorf("parse %s: %s", path, diags.Error())
		}
	}

	f.Body().SetAttributeValue(key, value)
	return os.WriteFile(path, f.Bytes(), 0o644)
}

// UpsertTfvarExpr is like UpsertTfvar but takes a raw HCL expression string
// (e.g. `["a", "b"]` or `{ key = "v" }`). Used when the variable is HCL=true.
func UpsertTfvarExpr(projectDir, key, expr string) error {
	path := filepath.Join(projectDir, "terraform.tfvars")
	src, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var f *hclwrite.File
	if len(src) == 0 {
		f = hclwrite.NewEmptyFile()
	} else {
		var diags hcl.Diagnostics
		f, diags = hclwrite.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			return fmt.Errorf("parse %s: %s", path, diags.Error())
		}
	}

	tokens := hclwrite.TokensForIdentifier(expr)
	f.Body().SetAttributeRaw(key, tokens)
	return os.WriteFile(path, f.Bytes(), 0o644)
}

// DeleteTfvar removes a key from terraform.tfvars. Missing key is not an error.
func DeleteTfvar(projectDir, key string) error {
	path := filepath.Join(projectDir, "terraform.tfvars")
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	f, diags := hclwrite.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return fmt.Errorf("parse %s: %s", path, diags.Error())
	}
	f.Body().RemoveAttribute(key)
	return os.WriteFile(path, f.Bytes(), 0o644)
}
