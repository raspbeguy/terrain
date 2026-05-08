package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RefreshWorkspaces queries tofu (or scans terraform.tfstate.d/) and updates the per-backend cache.
func (b *Backend) RefreshWorkspaces(ctx context.Context, projectID string) error {
	p, ok := b.projectByID(projectID)
	if !ok {
		return fmt.Errorf("project %q not registered with backend %q", projectID, b.id)
	}
	workDir, err := p.WorkingDir()
	if err != nil {
		return err
	}

	names := b.discoverWorkspaces(ctx, projectID, workDir)
	b.setWorkspaceCache(projectID, names)
	return nil
}

// discoverWorkspaces: tofu workspace list → terraform.tfstate.d/ scan → ["default"].
func (b *Backend) discoverWorkspaces(ctx context.Context, projectID, workDir string) []string {
	if names, ok := b.tofuWorkspaceList(ctx, projectID, workDir); ok {
		return sortWorkspaces(uniqueWithDefault(names))
	}
	if names := scanStateDirs(workDir); len(names) > 0 {
		return sortWorkspaces(uniqueWithDefault(names))
	}
	return []string{"default"}
}

func (b *Backend) tofuWorkspaceList(ctx context.Context, projectID, workDir string) ([]string, bool) {
	bin, err := b.workspaceBin(ctx, projectID)
	if err != nil {
		slog.Debug("workspace list: no binary", "project", projectID, "err", err)
		return nil, false
	}
	cmd := hostCommand(ctx, workDir, []string{"NO_COLOR=1"}, bin.Path, "workspace", "list", "-no-color")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Debug("workspace list failed",
			"project", projectID, "stderr", stderr.String(), "err", err)
		return nil, false
	}
	return parseWorkspaceList(stdout.String()), true
}

// parseWorkspaceList strips the "*" active marker and trims whitespace; defensive against tofu output drift.
func parseWorkspaceList(stdout string) []string {
	var out []string
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// scanStateDirs reads terraform.tfstate.d/ — the local-state backend's on-disk workspace layout.
func scanStateDirs(workDir string) []string {
	entries, err := os.ReadDir(filepath.Join(workDir, "terraform.tfstate.d"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("scanStateDirs", "dir", workDir, "err", err)
		}
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func uniqueWithDefault(in []string) []string {
	seen := map[string]bool{}
	out := []string{"default"}
	seen["default"] = true
	for _, n := range in {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// sortWorkspaces puts "default" first, the rest alphabetical.
func sortWorkspaces(names []string) []string {
	out := append([]string(nil), names...)
	sort.Slice(out, func(i, j int) bool {
		if out[i] == "default" {
			return true
		}
		if out[j] == "default" {
			return false
		}
		return out[i] < out[j]
	})
	return out
}

// CreateTofuWorkspace runs `tofu init` (if needed) then `tofu workspace new <name>`.
func (b *Backend) CreateTofuWorkspace(ctx context.Context, projectID, name string) error {
	if !IsValidWorkspaceName(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	p, ok := b.projectByID(projectID)
	if !ok {
		return fmt.Errorf("project %q not registered with backend %q", projectID, b.id)
	}
	workDir, err := p.WorkingDir()
	if err != nil {
		return err
	}
	bin, err := b.workspaceBin(ctx, projectID)
	if err != nil {
		return fmt.Errorf("resolve binary: %w", err)
	}
	if err := ensureTofuInit(ctx, workDir, bin); err != nil {
		return err
	}
	cmd := hostCommand(ctx, workDir, []string{"NO_COLOR=1"}, bin.Path,
		"workspace", "new", "-no-color", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tofu workspace new %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteTofuWorkspace switches to default first (tofu refuses to delete the active workspace) then removes the named one.
func (b *Backend) DeleteTofuWorkspace(ctx context.Context, projectID, name string) error {
	if name == "default" {
		return errors.New("the default workspace cannot be deleted")
	}
	if !IsValidWorkspaceName(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	p, ok := b.projectByID(projectID)
	if !ok {
		return fmt.Errorf("project %q not registered with backend %q", projectID, b.id)
	}
	workDir, err := p.WorkingDir()
	if err != nil {
		return err
	}
	bin, err := b.workspaceBin(ctx, projectID)
	if err != nil {
		return fmt.Errorf("resolve binary: %w", err)
	}
	selectCmd := hostCommand(ctx, workDir, []string{"NO_COLOR=1"}, bin.Path,
		"workspace", "select", "-no-color", "default")
	if out, err := selectCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tofu workspace select default: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	delCmd := hostCommand(ctx, workDir, []string{"NO_COLOR=1"}, bin.Path,
		"workspace", "delete", "-force", "-no-color", name)
	if out, err := delCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tofu workspace delete %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureTofuInit(ctx context.Context, workDir string, bin BinaryInfo) error {
	if _, err := os.Stat(filepath.Join(workDir, ".terraform")); err == nil {
		return nil
	}
	cmd := hostCommand(ctx, workDir, []string{"NO_COLOR=1"}, bin.Path,
		"init", "-input=false", "-no-color")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s init: %w (%s)", bin.Name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// workspaceBin resolves the binary using the project's "default" workspace settings as a stand-in.
func (b *Backend) workspaceBin(ctx context.Context, projectID string) (BinaryInfo, error) {
	wsID := b.id + ":" + projectID + ":default"
	settings, _ := LoadWorkspaceSettings(b.id, wsID)
	engine := settings.EffectiveManagedEngine(b.defaults.Engine)
	return b.binaryResolver(settings).Resolve(ctx, engine, settings.ManagedVersion)
}

func (b *Backend) projectByID(projectID string) (Project, bool) {
	for _, p := range b.projects {
		if p.ID == projectID {
			return p, true
		}
	}
	return Project{}, false
}

func (b *Backend) setWorkspaceCache(projectID string, names []string) {
	b.wsMu.Lock()
	defer b.wsMu.Unlock()
	if b.wsCache == nil {
		b.wsCache = map[string][]string{}
	}
	b.wsCache[projectID] = names
}

func (b *Backend) workspaceCache(projectID string) []string {
	b.wsMu.RLock()
	defer b.wsMu.RUnlock()
	if names, ok := b.wsCache[projectID]; ok {
		out := make([]string, len(names))
		copy(out, names)
		return out
	}
	return []string{"default"}
}
