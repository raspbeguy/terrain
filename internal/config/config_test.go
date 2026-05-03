package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMain swaps the system keyring backend for an in-memory mock so the
// secrets package's Set/Get/Delete work without a D-Bus session. All tests
// in this package see the same mock store; they must use unique IDs/keys
// to avoid bleeding into each other.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// withConfigDir points $XDG_CONFIG_HOME at a tmp dir for the test, so
// config.Path() and Save() write into a sandbox.
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
			{ID: "p1", Name: "infra", Path: "/home/dev/infra"},
		},
	})
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File must exist where we expect.
	expected := filepath.Join(dir, "terrain", "config.toml")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("config not at %s: %v", expected, err)
	}

	// Round-trip.
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
	if got.Backends[0].Projects[0].Path != "/home/dev/infra" {
		t.Errorf("project path round-trip failed: %+v", got.Backends[0].Projects[0])
	}
}

func TestAddLocalProject_CreatesBackendOnFirstCall(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()

	bc, p, err := c.AddLocalProject("infra", "/home/dev/infra")
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

	if _, _, err := c.AddLocalProject("a", "/home/dev/a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddLocalProject("b", "/home/dev/b"); err != nil {
		t.Fatal(err)
	}
	if len(c.Backends) != 1 {
		t.Errorf("expected 1 backend, got %d", len(c.Backends))
	}
	if len(c.Backends[0].Projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(c.Backends[0].Projects))
	}
}

func TestAddLocalProject_RelativePathBecomesAbsolute(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()

	rel := "./relative"
	_, p, err := c.AddLocalProject("rel", rel)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p.Path) {
		t.Errorf("expected absolute path, got %q", p.Path)
	}
}

func TestRemoveLocalProject_RemovesOneOfMany(t *testing.T) {
	withConfigDir(t)
	c := defaultConfig()
	_, a, err := c.AddLocalProject("a", "/home/dev/a")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := c.AddLocalProject("b", "/home/dev/b")
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
	_, p, err := c.AddLocalProject("only", "/home/dev/only")
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
	if _, _, err := c.AddLocalProject("a", "/home/dev/a"); err != nil {
		t.Fatal(err)
	}
	err := c.RemoveLocalProject("does-not-exist")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
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
	// Empty keyring → falls back to plaintext.
	if got := bc.ResolveToken(); got != "plaintext-fallback" {
		t.Errorf("no keyring: got %q, want plaintext-fallback", got)
	}
	// Populate keyring → keyring wins.
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
		{ID: "remote-B", Type: "remote", Token: ""}, // no plaintext, skip
	}

	n, err := c.MigrateTokens()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("first call: migrated %d, want 1", n)
	}

	// Second call: nothing to migrate.
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
				{ID: "p1", Name: "infra", Path: "/home/dev/infra"},
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
