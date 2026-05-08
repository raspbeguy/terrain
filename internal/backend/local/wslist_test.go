package local

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseWorkspaceList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single default active", "* default\n", []string{"default"}},
		{"three workspaces with active marker", "  default\n* staging\n  prod\n", []string{"default", "staging", "prod"}},
		{"crlf line endings", "  default\r\n* staging\r\n", []string{"default", "staging"}},
		{"trailing blank lines", "  default\n\n\n", []string{"default"}},
		{"empty input", "", nil},
	}
	for _, c := range cases {
		got := parseWorkspaceList(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parseWorkspaceList(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestScanStateDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateRoot := filepath.Join(dir, "terraform.tfstate.d")
	for _, ws := range []string{"staging", "prod", "ephemeral"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, ws), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "not-a-dir"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	got := scanStateDirs(dir)
	wantSet := map[string]bool{"staging": true, "prod": true, "ephemeral": true}
	if len(got) != len(wantSet) {
		t.Fatalf("scanStateDirs len = %d, want %d (%v)", len(got), len(wantSet), got)
	}
	for _, n := range got {
		if !wantSet[n] {
			t.Errorf("unexpected workspace %q", n)
		}
	}
}

func TestSortWorkspacesDefaultFirst(t *testing.T) {
	got := sortWorkspaces([]string{"prod", "default", "alpha", "staging"})
	want := []string{"default", "alpha", "prod", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortWorkspaces = %v, want %v", got, want)
	}
}

func TestUniqueWithDefault(t *testing.T) {
	got := uniqueWithDefault([]string{"staging", "default", "staging", "prod"})
	want := []string{"default", "staging", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueWithDefault = %v, want %v", got, want)
	}
}
