package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestLoad_NoFile_ReturnsDefaults(t *testing.T) {
	withConfigDir(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.DefaultEngine != "tofu" {
		t.Errorf("DefaultEngine: got %q, want tofu", cfg.App.DefaultEngine)
	}
	if len(cfg.Backends) != 0 {
		t.Errorf("expected empty backends, got %d", len(cfg.Backends))
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := withConfigDir(t)

	c := defaultConfig()
	c.App.DefaultEngine = "terraform"
	c.Backends = append(c.Backends, BackendConfig{
		ID:   "local",
		Type: "local",
		Name: "Local",
		Projects: []ProjectConfig{
			{ID: "p1", Name: "infra", GitURL: "https://example.com/infra.git", Subpath: "envs/prod"},
		},
	})
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	expected := filepath.Join(dir, "terrain", "config.toml")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("config not at %s: %v", expected, err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.App.DefaultEngine != "terraform" {
		t.Errorf("DefaultEngine: got %q", got.App.DefaultEngine)
	}
	if len(got.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(got.Backends))
	}
	p := got.Backends[0].Projects[0]
	if p.GitURL != "https://example.com/infra.git" || p.Subpath != "envs/prod" {
		t.Errorf("project round-trip failed: %+v", p)
	}
}

func mkProj(name, url, subpath string) ProjectConfig {
	return ProjectConfig{Name: name, GitURL: url, Subpath: subpath}
}

func TestAddLocalProject_CreatesBackendOnFirstCall(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()

	bc, p, err := c.AddLocalProject(mkProj("infra", "https://example.com/infra.git", ""))
	if err != nil {
		t.Fatalf("AddLocalProject: %v", err)
	}
	if bc.Type != "local" || bc.ID != "local" {
		t.Errorf("backend: %+v", bc)
	}
	if p.Name != "infra" || p.ID == "" {
		t.Errorf("project: %+v", p)
	}
	if len(c.Backends) != 1 || len(c.Backends[0].Projects) != 1 {
		t.Errorf("config not mutated: %+v", c.Backends)
	}
}

func TestAddLocalProject_AppendsToExistingLocal(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()

	if _, _, err := c.AddLocalProject(mkProj("a", "https://example.com/a.git", "")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddLocalProject(mkProj("b", "https://example.com/b.git", "")); err != nil {
		t.Fatal(err)
	}
	if len(c.Backends) != 1 {
		t.Errorf("expected 1 backend, got %d", len(c.Backends))
	}
	if len(c.Backends[0].Projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(c.Backends[0].Projects))
	}
}

func TestAddLocalProject_RejectsMissingURL(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()

	if _, _, err := c.AddLocalProject(ProjectConfig{Name: "no-url"}); err == nil {
		t.Errorf("expected error for empty git_url")
	}
}

func TestRemoveLocalProject_RemovesOneOfMany(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()
	_, a, err := c.AddLocalProject(mkProj("a", "https://example.com/a.git", ""))
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := c.AddLocalProject(mkProj("b", "https://example.com/b.git", ""))
	if err != nil {
		t.Fatal(err)
	}

	if err := c.RemoveLocalProject(a.ID); err != nil {
		t.Fatalf("RemoveLocalProject: %v", err)
	}
	if len(c.Backends) != 1 {
		t.Fatalf("expected local backend retained, got %d backends", len(c.Backends))
	}
	if len(c.Backends[0].Projects) != 1 || c.Backends[0].Projects[0].ID != b.ID {
		t.Errorf("expected only project b to remain, got %+v", c.Backends[0].Projects)
	}
}

func TestRemoveLocalProject_DropsBackendWhenEmpty(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()
	_, p, err := c.AddLocalProject(mkProj("only", "https://example.com/only.git", ""))
	if err != nil {
		t.Fatal(err)
	}

	if err := c.RemoveLocalProject(p.ID); err != nil {
		t.Fatalf("RemoveLocalProject: %v", err)
	}
	if len(c.Backends) != 0 {
		t.Errorf("expected empty backend list after last project removal, got %+v", c.Backends)
	}
}

func TestRemoveLocalProject_NotFound(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()
	if _, _, err := c.AddLocalProject(mkProj("a", "https://example.com/a.git", "")); err != nil {
		t.Fatal(err)
	}
	err := c.RemoveLocalProject("does-not-exist")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestLoad_DropsLegacyPathOnlyProject(t *testing.T) {
	dir := withConfigDir(t)
	cfgPath := filepath.Join(dir, "terrain", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `[[backend]]
id = "local"
type = "local"
name = "Local"

[[backend.projects]]
id = "p1"
name = "infra"
path = "/home/dev/infra"

[[backend.projects]]
id = "p2"
name = "git"
git_url = "https://example.com/git.git"
`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Backends) != 1 || len(c.Backends[0].Projects) != 1 {
		t.Fatalf("expected 1 project after legacy skip, got %+v", c.Backends)
	}
	if c.Backends[0].Projects[0].ID != "p2" {
		t.Errorf("expected p2 retained, got %+v", c.Backends[0].Projects[0])
	}
}

func TestAddRemoteBackend_StoresTokenInKeyring(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()

	bc, err := c.AddRemoteBackend("Prod TFE", "tfe", "https://tfe.example.com", "acme", "secret-token-xyz")
	if err != nil {
		t.Fatalf("AddRemoteBackend: %v", err)
	}
	if bc.Type != "remote" || bc.Token != "" {
		t.Errorf("token should not be in TOML when keyring works: %+v", bc)
	}
	if got := bc.ResolveToken(); got != "secret-token-xyz" {
		t.Errorf("ResolveToken: got %q, want secret-token-xyz", got)
	}
}

func TestResolveToken_PrefersKeyring(t *testing.T) {
	withConfigDir(t)
	bc := BackendConfig{
		ID:    "remote-1",
		Type:  "remote",
		Token: "plaintext-fallback",
	}
	if got := bc.ResolveToken(); got != "plaintext-fallback" {
		t.Errorf("no keyring: got %q, want plaintext-fallback", got)
	}
	c := defaultConfig()
	c.Backends = []BackendConfig{bc}
	if _, err := c.MigrateTokens(); err != nil {
		t.Fatalf("MigrateTokens: %v", err)
	}
	if c.Backends[0].Token != "" {
		t.Errorf("expected plaintext cleared after migrate, got %q", c.Backends[0].Token)
	}
	if got := c.Backends[0].ResolveToken(); got != "plaintext-fallback" {
		t.Errorf("after migrate: got %q, want plaintext-fallback", got)
	}
}

func TestMigrateTokens_Idempotent(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()
	c.Backends = []BackendConfig{
		{ID: "remote-A", Type: "remote", Token: "tok-A"},
		{ID: "remote-B", Type: "remote", Token: ""},
	}

	n, err := c.MigrateTokens()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("first call: migrated %d, want 1", n)
	}

	n, err = c.MigrateTokens()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second call: migrated %d, want 0", n)
	}
}

func TestBuildBackends_LocalOnly(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()
	c.Backends = []BackendConfig{
		{
			ID:   "local",
			Type: "local",
			Name: "Local",
			Projects: []ProjectConfig{
				{ID: "p1", Name: "infra", GitURL: "https://example.com/infra.git"},
			},
		},
	}

	backends, err := BuildBackends(c)
	if err != nil {
		t.Fatalf("BuildBackends: %v", err)
	}
	if len(backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(backends))
	}
	if backends[0].ID() != "local" {
		t.Errorf("backend ID: %q", backends[0].ID())
	}
}

func TestBuildBackends_UnknownType(t *testing.T) {
	c := defaultConfig()
	c.Backends = []BackendConfig{
		{ID: "weird", Type: "weird-type"},
	}
	_, err := BuildBackends(c)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
}

func TestBuildBackends_NilConfig(t *testing.T) {
	got, err := BuildBackends(nil)
	if err != nil {
		t.Fatalf("nil config should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil config should return empty slice, got %d", len(got))
	}
}
