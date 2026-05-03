package hcl

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// UpsertTfvarFile writes or replaces a single key=value entry in the tfvars
// file at path. Preserves comments and ordering of unrelated attributes
// (hclwrite round-trip). When the file doesn't exist it's created (and any
// missing parent directories with it).
//
// value is treated as a literal: scalar Go types (string/int/bool) become
// the obvious cty value; for HCL expression values (lists, objects, raw
// HCL) callers should use UpsertTfvarFileExpr.
func UpsertTfvarFile(path, key string, value cty.Value) error {
	f, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	f.Body().SetAttributeValue(key, value)
	return writeFile(path, f.Bytes())
}

// UpsertTfvarFileExpr is like UpsertTfvarFile but takes a raw HCL expression
// string (e.g. `["a", "b"]` or `{ key = "v" }`). Used when the variable is
// HCL=true.
func UpsertTfvarFileExpr(path, key, expr string) error {
	f, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	tokens := hclwrite.TokensForIdentifier(expr)
	f.Body().SetAttributeRaw(key, tokens)
	return writeFile(path, f.Bytes())
}

// DeleteTfvarFile removes a key from the tfvars file at path. Missing key
// or missing file are not errors.
func DeleteTfvarFile(path, key string) error {
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

// UpsertTfvar is the directory-based wrapper kept for callers that operate
// on a project directory's terraform.tfvars (tests, legacy uses). New code
// should prefer UpsertTfvarFile with an explicit absolute path.
func UpsertTfvar(projectDir, key string, value cty.Value) error {
	return UpsertTfvarFile(filepath.Join(projectDir, "terraform.tfvars"), key, value)
}

// UpsertTfvarExpr is the directory-based wrapper for UpsertTfvarFileExpr.
func UpsertTfvarExpr(projectDir, key, expr string) error {
	return UpsertTfvarFileExpr(filepath.Join(projectDir, "terraform.tfvars"), key, expr)
}

// DeleteTfvar is the directory-based wrapper for DeleteTfvarFile.
func DeleteTfvar(projectDir, key string) error {
	return DeleteTfvarFile(filepath.Join(projectDir, "terraform.tfvars"), key)
}

// readOrEmpty reads path as an hclwrite.File, returning an empty file when
// path doesn't exist. Bubbles up parse errors verbatim so callers can
// surface them to the user.
func readOrEmpty(path string) (*hclwrite.File, error) {
	src, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(src) == 0 {
		return hclwrite.NewEmptyFile(), nil
	}
	f, diags := hclwrite.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %s", path, diags.Error())
	}
	return f, nil
}

// writeFile creates parent directories as needed, then writes payload to
// path with 0644 permissions. The mkdir-all means callers can target a
// brand-new $XDG_DATA_HOME/terrain/.../overrides.tfvars without staging
// directory creation themselves.
func writeFile(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, payload, 0o644)
}
