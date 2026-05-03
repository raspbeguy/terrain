// Package config loads and persists Terrain's user-level registry — the list
// of backends and (for local backends) projects the GUI knows about. Stored
// at $XDG_CONFIG_HOME/terrain/config.toml.
//
// Credentials never live here: secrets are stored in libsecret keyed by
// "terrain/<backend-id>/<varset-id>/<key>" or similar.
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

// Config is the on-disk shape. Field names are TOML-tagged; struct field
// names follow Go conventions.
type Config struct {
	App      AppConfig       `toml:"app"`
	Backends []BackendConfig `toml:"backend"`
}

// AppConfig holds global preferences not tied to a single backend.
type AppConfig struct {
	// DefaultEngine is which CLI to prefer when both are installed.
	DefaultEngine string `toml:"default_engine"`
}

// BackendConfig describes one entry in the registry. Local and remote share
// the same struct because we want users to add either kind through the same
// flow; unused fields stay empty.
type BackendConfig struct {
	ID   string `toml:"id"`
	Type string `toml:"type"` // "local" or "remote"
	Name string `toml:"name"`

	// Local-only:
	Projects []ProjectConfig `toml:"projects,omitempty"`

	// Remote-only:
	Endpoint     string `toml:"endpoint,omitempty"`
	Organization string `toml:"organization,omitempty"`
	Flavor       string `toml:"flavor,omitempty"` // "otf", "hcp", "tfe"

	// Token is the API credential. Preferred storage is the system keyring
	// (libsecret on Linux); if the keyring is unavailable AddRemoteBackend
	// falls back to writing this field plaintext, with a warning logged so
	// the user knows. Empty here AND no keyring entry means we'll try the
	// TFE_TOKEN env var at backend-construction time.
	Token string `toml:"token,omitempty"`
}

// ProjectConfig is a registered local project — a directory containing .tf
// files. M1 ships one workspace ("default") per project; M2 will discover
// real workspace lists via `tofu workspace list`.
type ProjectConfig struct {
	ID   string `toml:"id"`
	Name string `toml:"name"`
	Path string `toml:"path"`
}

// Path returns the full path to the config file, honouring XDG_CONFIG_HOME.
func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "terrain", "config.toml"), nil
}

// Load reads the config from disk. Returns a default Config when the file
// doesn't exist yet (first run); other read/parse errors propagate.
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
	if c.App.DefaultEngine == "" {
		c.App.DefaultEngine = "tofu"
	}
	return c, nil
}

// Save writes the config back to disk, creating $XDG_CONFIG_HOME/terrain/ if
// needed. Atomic via write-rename.
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

// AddRemoteBackend appends a new remote backend (OTF/HCP/TFE) and persists.
// Each call creates a distinct backend entry — users can register multiple
// remote backends (e.g. an OTF dev instance and a separate prod TFE).
//
// Token storage prefers the system keyring; on keyring failure it logs a
// warning and falls back to writing the token plaintext into config.toml so
// the workflow still works on minimal/headless setups (CI, containers
// without D-Bus).
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

// ResolveToken returns the token for one backend, preferring keyring storage
// over plaintext. Returns "" if neither has a value (caller falls back to
// env var or rejects the backend).
func (bc BackendConfig) ResolveToken() string {
	if v, err := secrets.Get(secrets.TokenKey(bc.ID)); err == nil && v != "" {
		return v
	}
	return bc.Token
}

// MigrateTokens walks the backends and, for any with a non-empty plaintext
// Token field, tries to move that value into the system keyring. On success
// the plaintext is cleared from the in-memory config and persisted, leaving
// the keyring as the sole source.
//
// Idempotent: backends without a plaintext token are skipped; backends
// that already have a keyring entry get the keyring entry refreshed
// (defensive, in case of partial earlier migrations).
//
// Failures are non-fatal — if the keyring is unreachable we leave the
// plaintext in place and log a warning. Returns the number of tokens
// successfully migrated.
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

// RemoveLocalProject removes the project with the given ID from the local
// backend in the config and persists. If removal leaves the local backend
// with no projects, the local backend itself is dropped from the registry.
// Returns domain-style sentinel ErrProjectNotFound if no matching project
// exists. Does NOT touch on-disk artifacts (cache, state versions, keyring)
// — those persist so re-adding the same path keeps run history visible.
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

// ErrProjectNotFound is returned by RemoveLocalProject when the project ID
// isn't registered. Distinct from a save error so callers can distinguish a
// stale UI request from a disk failure.
var ErrProjectNotFound = errors.New("project not found")

// AddLocalBackend appends a new local backend with one project, persisting
// immediately. If a local backend already exists in the config, the project
// is appended to it (one local backend per registry).
func (c *Config) AddLocalProject(name, path string) (BackendConfig, ProjectConfig, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return BackendConfig{}, ProjectConfig{}, fmt.Errorf("abs %s: %w", path, err)
	}
	project := ProjectConfig{
		ID:   newID(),
		Name: name,
		Path: abs,
	}

	for i, bc := range c.Backends {
		if bc.Type == "local" {
			c.Backends[i].Projects = append(c.Backends[i].Projects, project)
			return c.Backends[i], project, c.Save()
		}
	}

	bc := BackendConfig{
		ID:       "local",
		Type:     "local",
		Name:     "Local",
		Projects: []ProjectConfig{project},
	}
	c.Backends = append(c.Backends, bc)
	return bc, project, c.Save()
}

func defaultConfig() *Config {
	return &Config{
		App: AppConfig{DefaultEngine: "tofu"},
	}
}
