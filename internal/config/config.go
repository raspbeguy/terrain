// Package config loads and persists $XDG_CONFIG_HOME/terrain/config.toml
// — the registry of backends and projects. Credentials live in
// libsecret, not here.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/raspbeguy/terrain/internal/secrets"
)

type Config struct {
	App      AppConfig       `toml:"app"`
	Backends []BackendConfig `toml:"backend"`
}

// AppConfig holds global preferences not tied to a single backend.
type AppConfig struct {
	// DefaultEngine is "tofu" or "terraform"; preferred when both exist.
	DefaultEngine string `toml:"default_engine"`

	// ContainerRuntimePath is the container CLI binary (podman, docker,
	// nerdctl, …). Empty disables container mode.
	ContainerRuntimePath string `toml:"container_runtime_path"`

	// DefaultRunMode is the mode for workspaces with no override:
	// "subprocess", "bubblewrap", or "container". Empty = subprocess.
	DefaultRunMode string `toml:"default_run_mode"`

	// DefaultImageTofu / DefaultImageTerraform are the engine-specific
	// container image fallbacks. Tag or @digest forms both work.
	DefaultImageTofu      string `toml:"default_image_tofu"`
	DefaultImageTerraform string `toml:"default_image_terraform"`
}

// BackendConfig is a single registry entry; local and remote share the
// struct, unused fields stay empty.
type BackendConfig struct {
	ID   string `toml:"id"`
	Type string `toml:"type"` // "local" or "remote"
	Name string `toml:"name"`

	Projects []ProjectConfig `toml:"projects,omitempty"`

	Endpoint     string `toml:"endpoint,omitempty"`
	Organization string `toml:"organization,omitempty"`
	Flavor       string `toml:"flavor,omitempty"` // "otf", "hcp", "tfe"

	// Token plaintext is a fallback when libsecret is unreachable;
	// AddRemoteBackend writes this with a logged warning. Resolve via
	// ResolveToken (keyring first); empty + no keyring → try TFE_TOKEN.
	Token string `toml:"token,omitempty"`
}

type ProjectConfig struct {
	ID      string `toml:"id"`
	Name    string `toml:"name"`
	GitURL  string `toml:"git_url"`
	GitRef  string `toml:"git_ref,omitempty"`
	Subpath string `toml:"subpath,omitempty"`

	// SSHKeyLabel selects an internal/sshkeys label for SSH URLs; HTTPS tokens live in libsecret keyed by host.
	SSHKeyLabel string `toml:"ssh_key_label,omitempty"`
	GitUsername string `toml:"git_username,omitempty"`
}

func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "terrain", "config.toml"), nil
}

// Load returns a default Config when the file doesn't exist (first run).
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	c := defaultConfig()
	if _, err := toml.Decode(string(data), c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.dropLegacyLocalProjects()
	if c.App.DefaultEngine == "" {
		c.App.DefaultEngine = "tofu"
	}
	if c.App.ContainerRuntimePath == "" {
		c.App.ContainerRuntimePath = "/usr/bin/podman"
	}
	if c.App.DefaultImageTofu == "" {
		c.App.DefaultImageTofu = "ghcr.io/opentofu/opentofu:latest"
	}
	if c.App.DefaultImageTerraform == "" {
		c.App.DefaultImageTerraform = "hashicorp/terraform:latest"
	}
	// DefaultRunMode stays empty when unset; the resolver treats "" as
	// subprocess.
	return c, nil
}

// Save writes via temp-file + rename.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "config-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return fmt.Errorf("encode config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// AddRemoteBackend prefers libsecret for token storage; on keyring
// failure it falls back to plaintext in config.toml with a logged
// warning so headless/CI setups still work.
func (c *Config) AddRemoteBackend(name, flavor, endpoint, organization, token string) (BackendConfig, error) {
	bc := BackendConfig{
		ID:           "remote-" + newID(),
		Type:         "remote",
		Name:         name,
		Flavor:       flavor,
		Endpoint:     endpoint,
		Organization: organization,
	}
	if token != "" {
		if err := secrets.Set(secrets.TokenKey(bc.ID), token); err != nil {
			slog.Warn("keyring unavailable, falling back to plaintext token",
				"err", err, "backend", bc.ID)
			bc.Token = token
		}
	}
	c.Backends = append(c.Backends, bc)
	return bc, c.Save()
}

// ResolveToken prefers keyring over plaintext; "" means the caller
// should try TFE_TOKEN env or reject the backend.
func (bc BackendConfig) ResolveToken() string {
	if v, err := secrets.Get(secrets.TokenKey(bc.ID)); err == nil && v != "" {
		return v
	}
	return bc.Token
}

// MigrateTokens moves plaintext tokens into the keyring and clears them
// from config.toml. Idempotent and best-effort: keyring failures leave
// the plaintext in place with a logged warning.
func (c *Config) MigrateTokens() (int, error) {
	if c == nil {
		return 0, nil
	}
	if !secrets.Available() {
		slog.Warn("token migration skipped: secret service unavailable")
		return 0, nil
	}

	migrated := 0
	dirty := false
	for i, bc := range c.Backends {
		if bc.Token == "" {
			continue
		}
		if err := secrets.Set(secrets.TokenKey(bc.ID), bc.Token); err != nil {
			slog.Warn("token migration failed for backend", "id", bc.ID, "err", err)
			continue
		}
		c.Backends[i].Token = ""
		migrated++
		dirty = true
		slog.Info("token migrated to keyring", "backend", bc.ID)
	}

	if !dirty {
		return 0, nil
	}
	if err := c.Save(); err != nil {
		return migrated, fmt.Errorf("persist migrated config: %w", err)
	}
	return migrated, nil
}

// RemoveLocalProject drops the local backend entirely if its last
// project is removed. On-disk artifacts (cache, state versions,
// keyring) are NOT touched — re-adding the same path keeps history.
func (c *Config) RemoveLocalProject(projectID string) error {
	for bi, bc := range c.Backends {
		if bc.Type != "local" {
			continue
		}
		for pi, p := range bc.Projects {
			if p.ID != projectID {
				continue
			}
			c.Backends[bi].Projects = append(bc.Projects[:pi], bc.Projects[pi+1:]...)
			if len(c.Backends[bi].Projects) == 0 {
				c.Backends = append(c.Backends[:bi], c.Backends[bi+1:]...)
			}
			return c.Save()
		}
	}
	return ErrProjectNotFound
}

// ErrProjectNotFound distinguishes a stale UI request from a disk
// failure in RemoveLocalProject.
var ErrProjectNotFound = errors.New("project not found")

// AddLocalProject reuses the single "local" backend if present, creates it otherwise.
func (c *Config) AddLocalProject(p ProjectConfig) (BackendConfig, ProjectConfig, error) {
	if p.GitURL == "" {
		return BackendConfig{}, ProjectConfig{}, fmt.Errorf("git_url is required")
	}
	if p.ID == "" {
		p.ID = newID()
	}

	for i, bc := range c.Backends {
		if bc.Type == "local" {
			c.Backends[i].Projects = append(c.Backends[i].Projects, p)
			return c.Backends[i], p, c.Save()
		}
	}

	bc := BackendConfig{
		ID:       "local",
		Type:     "local",
		Name:     "Local",
		Projects: []ProjectConfig{p},
	}
	c.Backends = append(c.Backends, bc)
	return bc, p, c.Save()
}

// dropLegacyLocalProjects: pre-git "path"-only entries lose their key on decode (go-toml ignores unknown fields) and surface as empty GitURL.
func (c *Config) dropLegacyLocalProjects() {
	for bi := range c.Backends {
		if c.Backends[bi].Type != "local" {
			continue
		}
		kept := c.Backends[bi].Projects[:0]
		for _, p := range c.Backends[bi].Projects {
			if p.GitURL == "" {
				slog.Warn("skipping legacy local project (path-only schema)",
					"id", p.ID, "name", p.Name)
				continue
			}
			kept = append(kept, p)
		}
		c.Backends[bi].Projects = kept
	}
}

func defaultConfig() *Config {
	return &Config{
		App: AppConfig{
			DefaultEngine:         "tofu",
			ContainerRuntimePath:  "/usr/bin/podman",
			DefaultImageTofu:      "ghcr.io/opentofu/opentofu:latest",
			DefaultImageTerraform: "hashicorp/terraform:latest",
		},
	}
}
