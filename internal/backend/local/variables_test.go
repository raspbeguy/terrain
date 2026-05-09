package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/raspbeguy/terrain/internal/domain"
)

func TestUpsertPlainVar_WritesOverridesNotProjectTfvars(t *testing.T) {
	keyring.MockInit()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	projectDir := t.TempDir()

	b := New("local", "Local")
	b.AddProject(Project{ID: "p1", Name: "infra", dirOverride: projectDir})
	wsID := "local:p1:default"

	v := domain.Variable{
		Key:      "region",
		Value:    "eu-west-1",
		Category: domain.VarCategoryTerraform,
	}
	if err := b.UpsertVariable(context.Background(), wsID, v); err != nil {
		t.Fatalf("UpsertVariable: %v", err)
	}

	// Project terraform.tfvars must not have been created.
	if _, err := os.Stat(filepath.Join(projectDir, "terraform.tfvars")); err == nil {
		t.Errorf("project terraform.tfvars was created; terrain shouldn't write there")
	}

	// Overrides file must exist with the value.
	op := filepath.Join(dataHome, "terrain", "local", sanitize(wsID), "overrides.tfvars")
	data, err := os.ReadFile(op)
	if err != nil {
		t.Fatalf("read overrides: %v", err)
	}
	if !strings.Contains(string(data), `region = "eu-west-1"`) {
		t.Errorf("expected region in overrides file, got %q", data)
	}
}

func TestVariablesForWorkspace_ReadsOverrides(t *testing.T) {
	keyring.MockInit()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	projectDir := t.TempDir()

	// Declare `count` in source so it's marked Declared=true, then override.
	declSrc := `variable "count" { default = 1 }
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.tf"), []byte(declSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	b := New("local", "Local")
	b.AddProject(Project{ID: "p1", Name: "infra", dirOverride: projectDir})
	wsID := "local:p1:default"

	if err := b.UpsertVariable(context.Background(), wsID, domain.Variable{
		Key:      "count",
		Value:    "5",
		Category: domain.VarCategoryTerraform,
	}); err != nil {
		t.Fatalf("UpsertVariable: %v", err)
	}

	got, err := b.VariablesForWorkspace(context.Background(), wsID)
	if err != nil {
		t.Fatalf("VariablesForWorkspace: %v", err)
	}
	var cv *domain.Variable
	for i := range got {
		if got[i].Key == "count" {
			cv = &got[i]
		}
	}
	if cv == nil {
		t.Fatalf("count variable missing from listing")
	}
	if cv.Value != "5" {
		t.Errorf("expected overridden value 5, got %q", cv.Value)
	}
	if !cv.Declared {
		t.Errorf("expected Declared=true (variable is in source), got false")
	}
}

func TestDeleteVariable_RemovesFromOverridesOnly(t *testing.T) {
	keyring.MockInit()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	projectDir := t.TempDir()

	// Pre-populate the project's own terraform.tfvars; terrain must NOT
	// touch it on Delete.
	pretfvars := `keep = "user-set"
`
	if err := os.WriteFile(filepath.Join(projectDir, "terraform.tfvars"), []byte(pretfvars), 0o644); err != nil {
		t.Fatal(err)
	}

	b := New("local", "Local")
	b.AddProject(Project{ID: "p1", Name: "infra", dirOverride: projectDir})
	wsID := "local:p1:default"

	if err := b.UpsertVariable(context.Background(), wsID, domain.Variable{
		Key: "extra", Value: "added", Category: domain.VarCategoryTerraform,
	}); err != nil {
		t.Fatalf("UpsertVariable: %v", err)
	}
	if err := b.DeleteVariable(context.Background(), wsID, "extra"); err != nil {
		t.Fatalf("DeleteVariable: %v", err)
	}
	// Project tfvars must be untouched.
	got, err := os.ReadFile(filepath.Join(projectDir, "terraform.tfvars"))
	if err != nil {
		t.Fatalf("project tfvars disappeared: %v", err)
	}
	if string(got) != pretfvars {
		t.Errorf("project tfvars was modified by terrain, got %q", got)
	}
	// Overrides file should no longer mention 'extra'.
	op := filepath.Join(dataHome, "terrain", "local", sanitize(wsID), "overrides.tfvars")
	if data, err := os.ReadFile(op); err == nil && strings.Contains(string(data), "extra") {
		t.Errorf("overrides still has 'extra' after delete: %q", data)
	}
}
