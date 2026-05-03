package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestResolvedVars_WriteVarFile_Empty(t *testing.T) {
	t.Parallel()
	rv := &resolvedVars{Terraform: map[string]string{}}
	dir := t.TempDir()
	path, err := rv.writeVarFile(dir)
	if err != nil {
		t.Fatalf("writeVarFile error: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path for empty vars, got %q", path)
	}
	// File shouldn't exist either.
	if _, err := os.Stat(filepath.Join(dir, "vars.auto.tfvars.json")); err == nil {
		t.Fatal("var file shouldn't exist when no vars to write")
	}
}

func TestResolvedVars_WriteVarFile(t *testing.T) {
	t.Parallel()
	rv := &resolvedVars{
		Terraform: map[string]string{
			"db_password": "s3cret",
			"api_token":   "xyz",
		},
	}
	dir := t.TempDir()
	path, err := rv.writeVarFile(dir)
	if err != nil {
		t.Fatalf("writeVarFile error: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["db_password"] != "s3cret" || got["api_token"] != "xyz" {
		t.Errorf("unexpected contents: %+v", got)
	}

	// File permissions: must be 0600 — sensitive payload.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 perms, got %o", info.Mode().Perm())
	}
}

func TestResolvedVars_EnvEntries(t *testing.T) {
	t.Parallel()
	rv := &resolvedVars{
		Env: map[string]string{
			"AWS_ACCESS_KEY_ID":     "AKIA...",
			"AWS_SECRET_ACCESS_KEY": "...",
		},
	}
	got := rv.envEntries()
	sort.Strings(got)
	want := []string{
		"AWS_ACCESS_KEY_ID=AKIA...",
		"AWS_SECRET_ACCESS_KEY=...",
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestEnvIndex_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const ws = "ws-1"

	// empty initially
	if names, _ := loadEnvIndex(dir, ws); names != nil {
		t.Fatalf("expected empty, got %v", names)
	}

	if err := addEnvVar(dir, ws, "FOO"); err != nil {
		t.Fatal(err)
	}
	if err := addEnvVar(dir, ws, "BAR"); err != nil {
		t.Fatal(err)
	}
	if err := addEnvVar(dir, ws, "FOO"); err != nil {
		t.Fatal(err) // idempotent
	}

	names, err := loadEnvIndex(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("expected 2 entries, got %v", names)
	}

	if err := removeEnvVar(dir, ws, "FOO"); err != nil {
		t.Fatal(err)
	}
	names, _ = loadEnvIndex(dir, ws)
	if len(names) != 1 || names[0] != "BAR" {
		t.Errorf("expected [BAR], got %v", names)
	}
}
