package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/raspbeguy/terrain/internal/domain"
)

// withXDGConfig points $XDG_CONFIG_HOME at a tmp dir for the duration of t.
// All varsets persistence keys off os.UserConfigDir which honors the env var.
func withXDGConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestVarsets_CreateListDelete(t *testing.T) {
	withXDGConfig(t)
	b := New("local-test", "Local")

	ctx := context.Background()

	// Empty registry: List returns nothing without error.
	sets, err := b.VariableSets(ctx)
	if err != nil {
		t.Fatalf("VariableSets initial: %v", err)
	}
	if len(sets) != 0 {
		t.Fatalf("expected empty list, got %d", len(sets))
	}

	// Create
	a, err := b.CreateVariableSet(ctx, "AWS prod", "credentials")
	if err != nil {
		t.Fatalf("CreateVariableSet: %v", err)
	}
	if a.ID == "" || a.Name != "AWS prod" {
		t.Fatalf("created set malformed: %+v", a)
	}
	bSet, err := b.CreateVariableSet(ctx, "Common", "")
	if err != nil {
		t.Fatal(err)
	}

	// List
	sets, err = b.VariableSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 {
		t.Fatalf("expected 2 sets, got %d", len(sets))
	}
	// Order: sorted by name asc; "AWS prod" then "Common".
	if sets[0].Name != "AWS prod" || sets[1].Name != "Common" {
		t.Errorf("unexpected order: %v / %v", sets[0].Name, sets[1].Name)
	}

	// Read by ID
	got, err := b.VariableSet(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID {
		t.Errorf("VariableSet returned wrong id: %v vs %v", got.ID, a.ID)
	}

	// Delete one
	if err := b.DeleteVariableSet(ctx, bSet.ID); err != nil {
		t.Fatal(err)
	}
	sets, _ = b.VariableSets(ctx)
	if len(sets) != 1 || sets[0].ID != a.ID {
		t.Errorf("expected only %s left, got %v", a.ID, sets)
	}
}

func TestVarsets_UpsertAndDeleteVar(t *testing.T) {
	withXDGConfig(t)
	b := New("local-test", "Local")

	set, err := b.CreateVariableSet(context.Background(), "Common", "")
	if err != nil {
		t.Fatal(err)
	}

	// Upsert plain variable.
	v := domain.Variable{
		Key:      "region",
		Value:    "eu-west-1",
		Category: domain.VarCategoryTerraform,
	}
	if err := b.UpsertVariableSetVar(context.Background(), set.ID, v); err != nil {
		t.Fatalf("UpsertVariableSetVar: %v", err)
	}

	got, _ := b.VariableSet(context.Background(), set.ID)
	if len(got.Variables) != 1 || got.Variables[0].Key != "region" || got.Variables[0].Value != "eu-west-1" {
		t.Fatalf("unexpected vars after upsert: %+v", got.Variables)
	}

	// Update the same key; should replace, not append.
	v.Value = "us-east-2"
	if err := b.UpsertVariableSetVar(context.Background(), set.ID, v); err != nil {
		t.Fatal(err)
	}
	got, _ = b.VariableSet(context.Background(), set.ID)
	if len(got.Variables) != 1 || got.Variables[0].Value != "us-east-2" {
		t.Fatalf("expected replace, got %+v", got.Variables)
	}

	// Add a second key.
	if err := b.UpsertVariableSetVar(context.Background(), set.ID, domain.Variable{
		Key:      "instance_count",
		Value:    "3",
		Category: domain.VarCategoryTerraform,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = b.VariableSet(context.Background(), set.ID)
	if len(got.Variables) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(got.Variables))
	}

	// Delete one.
	if err := b.DeleteVariableSetVar(context.Background(), set.ID, "region"); err != nil {
		t.Fatal(err)
	}
	got, _ = b.VariableSet(context.Background(), set.ID)
	if len(got.Variables) != 1 || got.Variables[0].Key != "instance_count" {
		t.Errorf("expected just instance_count, got %+v", got.Variables)
	}
}

func TestVarsets_ManifestPath(t *testing.T) {
	dir := withXDGConfig(t)
	b := New("local-test", "Local")

	set, err := b.CreateVariableSet(context.Background(), "S", "")
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, "terrain", "varsets", set.ID+".json")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("manifest not at expected path %s: %v", expected, err)
	}
	_ = b
}

func TestVarsets_UpdateMeta(t *testing.T) {
	withXDGConfig(t)
	b := New("local-test", "Local")

	set, _ := b.CreateVariableSet(context.Background(), "Original", "")

	meta := domain.VariableSet{
		Name:        "Renamed",
		Description: "new desc",
		Scope:       domain.ScopeWorkspace,
		Workspaces:  []string{"ws-1", "ws-2"},
		Priority:    5,
	}
	if err := b.UpdateVariableSetMeta(context.Background(), set.ID, meta); err != nil {
		t.Fatal(err)
	}

	got, _ := b.VariableSet(context.Background(), set.ID)
	if got.Name != "Renamed" || got.Description != "new desc" {
		t.Errorf("name/desc not updated: %+v", got)
	}
	if got.Scope != domain.ScopeWorkspace {
		t.Errorf("scope not updated: %v", got.Scope)
	}
	if len(got.Workspaces) != 2 || got.Workspaces[0] != "ws-1" || got.Workspaces[1] != "ws-2" {
		t.Errorf("workspaces not updated: %+v", got.Workspaces)
	}
	if got.Priority != 5 {
		t.Errorf("priority not updated: %d", got.Priority)
	}
}
