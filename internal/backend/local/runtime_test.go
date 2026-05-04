package local

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTranslateArgs(t *testing.T) {
	t.Parallel()
	mounts := map[string]string{
		"/home/u/proj":            "/workspace",
		"/var/cache/terrain/r1":   "/terrain/run",
	}
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "plan flags get rewritten, non-path args pass through",
			in: []string{
				"plan", "-json", "-input=false",
				"-out=/var/cache/terrain/r1/plan.tfplan",
				"-var-file=/var/cache/terrain/r1/vars.auto.tfvars",
			},
			want: []string{
				"plan", "-json", "-input=false",
				"-out=/terrain/run/plan.tfplan",
				"-var-file=/terrain/run/vars.auto.tfvars",
			},
		},
		{
			name: "apply with positional plan-file path",
			in: []string{
				"apply", "-json", "-input=false",
				"/var/cache/terrain/r1/plan.tfplan",
			},
			want: []string{
				"apply", "-json", "-input=false",
				"/terrain/run/plan.tfplan",
			},
		},
		{
			name: "untranslatable absolute path passes through unchanged",
			in: []string{
				"-out=/some/other/path/plan.tfplan",
			},
			want: []string{
				"-out=/some/other/path/plan.tfplan",
			},
		},
		{
			name: "longest prefix wins when one mount is parent of another",
			in: []string{
				"-var-file=/home/u/proj/sub/extra.tfvars",
			},
			want: []string{
				// /home/u/proj is the only matching mount → /workspace prefix.
				"-var-file=/workspace/sub/extra.tfvars",
			},
		},
		{
			name: "non-path -target / -replace / -var get passthrough",
			in: []string{
				"-target=null_resource.foo",
				"-replace=null_resource.bar",
				"-var", "region=us-east-1",
			},
			want: []string{
				"-target=null_resource.foo",
				"-replace=null_resource.bar",
				"-var", "region=us-east-1",
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := translateArgs(tc.in, mounts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("translateArgs(%v) =\n  got %v\n  want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestWorkspaceSettings_RoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const (
		backend = "local"
		ws      = "local:p1:default"
	)

	// Empty initially — file doesn't exist; LoadWorkspaceSettings returns
	// zero value without error.
	got, err := LoadWorkspaceSettings(backend, ws)
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if (got != WorkspaceSettings{}) {
		t.Errorf("expected zero value, got %+v", got)
	}

	// Save non-empty, read back.
	want := WorkspaceSettings{
		RunMode: RunModeContainer,
		Image:   "ghcr.io/opentofu/opentofu:1.7",
	}
	if err := SaveWorkspaceSettings(backend, ws, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = LoadWorkspaceSettings(backend, ws)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}

	// Saving zero value removes the file.
	if err := SaveWorkspaceSettings(backend, ws, WorkspaceSettings{}); err != nil {
		t.Fatalf("Save zero: %v", err)
	}
	path, _ := workspaceSettingsPath(backend, ws)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected settings file to be removed, stat err = %v", err)
	}
}

func TestWorkspaceSettings_CorruptFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const (
		backend = "local"
		ws      = "ws-bad"
	)
	path, err := workspaceSettingsPath(backend, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadWorkspaceSettings(backend, ws)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestNewRuntime_HostByDefault(t *testing.T) {
	rt, err := newRuntime(runtimeOptions{RunMode: RunModeSubprocess})
	if err != nil {
		t.Fatalf("newRuntime subprocess: %v", err)
	}
	if _, ok := rt.(hostRuntime); !ok {
		t.Errorf("expected hostRuntime, got %T", rt)
	}
}

func TestNewRuntime_ContainerRequiresBinAndImage(t *testing.T) {
	if _, err := newRuntime(runtimeOptions{RunMode: RunModeContainer}); err == nil {
		t.Error("expected error when RuntimeBin empty, got nil")
	}
	if _, err := newRuntime(runtimeOptions{
		RunMode:    RunModeContainer,
		RuntimeBin: "podman",
		Image:      "",
	}); err == nil {
		t.Error("expected error when Image empty, got nil")
	}
}
