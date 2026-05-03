package local

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestResolvedVars_WriteVarFile_Empty(t *testing.T) {
	t.Parallel()
	rv := &resolvedVars{Terraform: map[string]termValue{}}
	dir := t.TempDir()
	path, err := rv.writeVarFile(dir)
	if err != nil {
		t.Fatalf("writeVarFile error: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path for empty vars, got %q", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "vars.auto.tfvars")); err == nil {
		t.Fatal("var file shouldn't exist when no vars to write")
	}
}

func TestResolvedVars_WriteVarFile_PreservesTypes(t *testing.T) {
	t.Parallel()
	count := cty.NumberIntVal(5)
	tags := cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")})
	rv := &resolvedVars{
		Terraform: map[string]termValue{
			"api_token":  {raw: "xyz"},                   // plain string → quoted
			"region":     {raw: "us-east-1"},             // plain string → quoted
			"count":      {cty: &count},                  // typed number → number literal
			"tags":       {cty: &tags},                   // typed list → HCL list
			"raw_object": {raw: `{ k = "v" }`, hcl: true}, // raw HCL expr → verbatim
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
	s := string(data)

	// hclwrite right-aligns `=` when multiple attributes share a body, so
	// we can't assert exact spacing — match key + value with any run of
	// whitespace between them.
	matches := func(pat string) bool {
		return regexp.MustCompile(pat).MatchString(s)
	}
	if !matches(`api_token\s*=\s*"xyz"`) {
		t.Errorf("api_token not quoted: %q", s)
	}
	if !matches(`count\s*=\s*5(\s|$)`) || strings.Contains(s, `"5"`) {
		t.Errorf("count should be a bare number 5: %q", s)
	}
	if !matches(`tags\s*=\s*\["a", "b"\]`) {
		t.Errorf("tags not a proper list: %q", s)
	}
	if !matches(`raw_object\s*=\s*\{ k = "v" \}`) {
		t.Errorf("raw HCL not preserved: %q", s)
	}

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
	// Not parallel: env-index lives under XDG_DATA_HOME, which we point at
	// a tmp dir via t.Setenv (forbidden alongside t.Parallel).
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const backendID = "local"
	const ws = "ws-1"

	if names, _ := loadEnvIndex(backendID, ws); names != nil {
		t.Fatalf("expected empty, got %v", names)
	}

	if err := addEnvVar(backendID, ws, "FOO"); err != nil {
		t.Fatal(err)
	}
	if err := addEnvVar(backendID, ws, "BAR"); err != nil {
		t.Fatal(err)
	}
	if err := addEnvVar(backendID, ws, "FOO"); err != nil {
		t.Fatal(err) // idempotent
	}

	names, err := loadEnvIndex(backendID, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("expected 2 entries, got %v", names)
	}

	if err := removeEnvVar(backendID, ws, "FOO"); err != nil {
		t.Fatal(err)
	}
	names, _ = loadEnvIndex(backendID, ws)
	if len(names) != 1 || names[0] != "BAR" {
		t.Errorf("expected [BAR], got %v", names)
	}
}
