package hcl

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// UpsertTfvarFile preserves comments and unrelated attributes via hclwrite
// round-trip. For non-scalar/raw HCL values use UpsertTfvarFileExpr.
func UpsertTfvarFile(path, key string, value cty.Value) error {
	f, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	f.Body().SetAttributeValue(key, value)
	return writeFile(path, f.Bytes())
}

// UpsertTfvarFileExpr takes a raw HCL expression (`["a", "b"]`, `{ k = "v" }`).
func UpsertTfvarFileExpr(path, key, expr string) error {
	f, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	tokens := hclwrite.TokensForIdentifier(expr)
	f.Body().SetAttributeRaw(key, tokens)
	return writeFile(path, f.Bytes())
}

// DeleteTfvarFile: missing key or file is a no-op.
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

func UpsertTfvar(projectDir, key string, value cty.Value) error {
	return UpsertTfvarFile(filepath.Join(projectDir, "terraform.tfvars"), key, value)
}

func UpsertTfvarExpr(projectDir, key, expr string) error {
	return UpsertTfvarFileExpr(filepath.Join(projectDir, "terraform.tfvars"), key, expr)
}

func DeleteTfvar(projectDir, key string) error {
	return DeleteTfvarFile(filepath.Join(projectDir, "terraform.tfvars"), key)
}

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

// writeFile mkdir's parents; lets callers target fresh out-of-project paths.
func writeFile(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, payload, 0o644)
}
