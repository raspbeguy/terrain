package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/secrets"
)

// varsetManifest is the on-disk shape of a variable set. One file per set
// at $XDG_CONFIG_HOME/terrain/varsets/<id>.json. We store names + values
// inline; sensitive values get the @secret sentinel and the real value lives
// in libsecret keyed by varsetSecretKey().
//
// Keeping each set in its own file (rather than one omnibus index file)
// keeps manual edits / deletes / renames simple, and limits blast radius
// when one file gets corrupted.
type varsetManifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Scope       string            `json:"scope"`            // "global" for now; project/workspace later
	Workspaces  []string          `json:"workspaces,omitempty"`
	ProjectID   string            `json:"project_id,omitempty"`
	Priority    int               `json:"priority,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Vars        []varsetVarRecord `json:"vars"`
}

type varsetVarRecord struct {
	Key         string `json:"key"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	HCL         bool   `json:"hcl,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Category    string `json:"category"` // "terraform" or "env"
}

const varsetSecretSentinel = "@terrain:varset-secret"

func varsetSecretKey(setID, key string) string {
	return "varset/" + setID + "/" + key
}

func varsetEnvKey(setID, key string) string {
	return "varset-env/" + setID + "/" + key
}

func varsetsDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(cfg, "terrain", "varsets"), nil
}

func varsetPath(id string) (string, error) {
	dir, err := varsetsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

// VariableSets returns all global variable sets the local backend knows
// about. We scan the varsets directory; corrupt files are skipped silently
// (with a warning logged in the runner) so one bad file doesn't break the
// management page.
func (b *Backend) VariableSets(_ context.Context) ([]domain.VariableSet, error) {
	dir, err := varsetsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read varsets dir: %w", err)
	}

	var out []domain.VariableSet
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		m, err := readVarsetManifest(path)
		if err != nil {
			continue
		}
		out = append(out, m.toDomain(b.id))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// VariableSet returns a single set by ID, populated with its variables.
func (b *Backend) VariableSet(_ context.Context, setID string) (domain.VariableSet, error) {
	path, err := varsetPath(setID)
	if err != nil {
		return domain.VariableSet{}, err
	}
	m, err := readVarsetManifest(path)
	if err != nil {
		return domain.VariableSet{}, err
	}
	return m.toDomain(b.id), nil
}

// CreateVariableSet writes a new manifest with a generated ID. Returns the
// created set so the UI can link to it. Persists immediately.
func (b *Backend) CreateVariableSet(_ context.Context, name, description string) (domain.VariableSet, error) {
	if strings.TrimSpace(name) == "" {
		return domain.VariableSet{}, errors.New("name is required")
	}
	id := "vs-" + newRunID() // reuse the time-prefixed hex helper
	now := time.Now()
	m := varsetManifest{
		ID:          id,
		Name:        name,
		Description: description,
		Scope:       string(domain.ScopeGlobal),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := writeVarsetManifest(m); err != nil {
		return domain.VariableSet{}, err
	}
	return m.toDomain(b.id), nil
}

// UpdateVariableSetMeta applies a metadata change to an existing set without
// touching its variables. Used by the management dialog when the user edits
// name / description / scope / attachments. Atomic write-rename on the
// manifest.
func (b *Backend) UpdateVariableSetMeta(_ context.Context, setID string, meta domain.VariableSet) error {
	path, err := varsetPath(setID)
	if err != nil {
		return err
	}
	m, err := readVarsetManifest(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) != "" {
		m.Name = meta.Name
	}
	m.Description = meta.Description
	if meta.Scope != "" {
		m.Scope = string(meta.Scope)
	}
	m.Workspaces = meta.Workspaces
	m.ProjectID = meta.ProjectID
	m.Priority = meta.Priority
	m.UpdatedAt = time.Now()
	return writeVarsetManifest(m)
}

// ListProjects returns the registered local projects so the varsets dialog
// can populate a project combo for the project-scoped varset case. Local-
// only — remote backends model "projects" differently (TFE projects are
// API objects).
func (b *Backend) ListProjects(_ context.Context) []domain.ProjectChoice {
	out := make([]domain.ProjectChoice, 0, len(b.projects))
	for _, p := range b.projects {
		out = append(out, domain.ProjectChoice{ID: p.ID, Name: p.Name})
	}
	return out
}

// DeleteVariableSet removes the manifest and any associated keyring entries
// for sensitive / env vars in the set.
func (b *Backend) DeleteVariableSet(_ context.Context, setID string) error {
	path, err := varsetPath(setID)
	if err != nil {
		return err
	}
	m, err := readVarsetManifest(path)
	if err != nil {
		// File missing or corrupt — just remove the path entry too.
		_ = os.Remove(path)
		return nil
	}
	for _, v := range m.Vars {
		if v.Sensitive {
			_ = secrets.Delete(varsetSecretKey(setID, v.Key))
		}
		if v.Category == string(domain.VarCategoryEnvironment) {
			_ = secrets.Delete(varsetEnvKey(setID, v.Key))
		}
	}
	return os.Remove(path)
}

// UpsertVariableSetVar adds or replaces a variable inside a set. Sensitive
// values route to libsecret with a sentinel placeholder in the manifest;
// env-category vars route to libsecret too (different keyring namespace).
func (b *Backend) UpsertVariableSetVar(_ context.Context, setID string, v domain.Variable) error {
	if strings.TrimSpace(v.Key) == "" {
		return errors.New("key is required")
	}
	path, err := varsetPath(setID)
	if err != nil {
		return err
	}
	m, err := readVarsetManifest(path)
	if err != nil {
		return err
	}

	rec := varsetVarRecord{
		Key:         v.Key,
		Description: v.Description,
		HCL:         v.HCL,
		Sensitive:   v.Sensitive,
		Category:    string(v.Category),
	}
	if rec.Category == "" {
		rec.Category = string(domain.VarCategoryTerraform)
	}

	// Defensive cleanup: clear ALL prior keyring entries for this key
	// before writing the new one, so a category transition (env→terraform,
	// sensitive→plain, etc.) doesn't leave a stale value in the other
	// namespace. Idempotent — Delete on a missing key is a no-op.
	_ = secrets.Delete(varsetSecretKey(setID, v.Key))
	_ = secrets.Delete(varsetEnvKey(setID, v.Key))

	switch {
	case rec.Category == string(domain.VarCategoryEnvironment):
		// Env vars always route to keyring; manifest stores a sentinel.
		if err := secrets.Set(varsetEnvKey(setID, v.Key), v.Value); err != nil {
			return fmt.Errorf("store env value: %w", err)
		}
		rec.Value = ""
	case v.Sensitive:
		if err := secrets.Set(varsetSecretKey(setID, v.Key), v.Value); err != nil {
			return fmt.Errorf("store sensitive value: %w", err)
		}
		rec.Value = varsetSecretSentinel
	default:
		rec.Value = v.Value
	}

	// Replace existing entry by key, else append.
	replaced := false
	for i, existing := range m.Vars {
		if existing.Key == v.Key {
			m.Vars[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		m.Vars = append(m.Vars, rec)
	}
	m.UpdatedAt = time.Now()
	return writeVarsetManifest(m)
}

// DeleteVariableSetVar removes one variable from a set. Idempotent.
func (b *Backend) DeleteVariableSetVar(_ context.Context, setID, key string) error {
	path, err := varsetPath(setID)
	if err != nil {
		return err
	}
	m, err := readVarsetManifest(path)
	if err != nil {
		return err
	}
	out := make([]varsetVarRecord, 0, len(m.Vars))
	for _, v := range m.Vars {
		if v.Key == key {
			if v.Sensitive {
				_ = secrets.Delete(varsetSecretKey(setID, key))
			}
			if v.Category == string(domain.VarCategoryEnvironment) {
				_ = secrets.Delete(varsetEnvKey(setID, key))
			}
			continue
		}
		out = append(out, v)
	}
	m.Vars = out
	m.UpdatedAt = time.Now()
	return writeVarsetManifest(m)
}

func readVarsetManifest(path string) (varsetManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return varsetManifest{}, err
	}
	var m varsetManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return varsetManifest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func writeVarsetManifest(m varsetManifest) error {
	dir, err := varsetsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, m.ID+".json")
	tmp, err := os.CreateTemp(dir, m.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (m varsetManifest) toDomain(backendID string) domain.VariableSet {
	scope := domain.VariableScope(m.Scope)
	if scope == "" {
		scope = domain.ScopeGlobal
	}
	out := domain.VariableSet{
		ID:          m.ID,
		BackendID:   backendID,
		Name:        m.Name,
		Description: m.Description,
		Scope:       scope,
		Workspaces:  m.Workspaces,
		ProjectID:   m.ProjectID,
		Priority:    m.Priority,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
	for _, r := range m.Vars {
		v := domain.Variable{
			Key:         r.Key,
			Value:       r.Value,
			Description: r.Description,
			HCL:         r.HCL,
			Sensitive:   r.Sensitive,
			Category:    domain.VariableCategory(r.Category),
		}
		if v.Category == "" {
			v.Category = domain.VarCategoryTerraform
		}
		// Sensitive vars have their plaintext in the keyring; the manifest
		// holds a sentinel placeholder. Always blank the value before
		// returning so the UI never displays the sentinel — even if a
		// non-sensitive var coincidentally matched the sentinel string,
		// `r.Sensitive=false` keeps its real value here.
		if v.Sensitive {
			v.Value = ""
		}
		out.Variables = append(out.Variables, v)
	}
	return out
}
