// Package ui hosts the GTK-side of terrain. Boundary rule: ui (and
// subpackages) is the only place allowed to import gotk4.
package ui

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/local"
	"github.com/raspbeguy/terrain/internal/config"
	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/gitutils"
	"github.com/raspbeguy/terrain/internal/runner"
	"github.com/raspbeguy/terrain/internal/ui/dialogs"
	"github.com/raspbeguy/terrain/internal/ui/window"
)

const AppID = "io.github.raspbeguy.Terrain"

type App struct {
	app *adw.Application

	cfg      *config.Config
	backends []domain.Backend
	window   *window.Window
	locks    *runner.WorkspaceLocks
}

func NewApp() *App {
	return &App{
		app:   adw.NewApplication(AppID, gio.ApplicationFlagsNone),
		locks: runner.NewWorkspaceLocks(),
	}
}

// Run blocks until the application exits.
func (a *App) Run(args []string) int {
	a.app.ConnectActivate(a.onActivate)
	a.registerActions()
	return a.app.Run(args)
}

// registerActions wires "app.*" actions referenced from Blueprint.
// Handlers may fire before onActivate (e.g. via `gapplication action`)
// so they must not assume the window exists.
func (a *App) registerActions() {
	addLocal := gio.NewSimpleAction("add-local-project", nil)
	addLocal.ConnectActivate(func(_ *glib.Variant) {
		a.onAddLocalProject()
	})
	a.app.AddAction(addLocal)

	addRemote := gio.NewSimpleAction("add-remote-backend", nil)
	addRemote.ConnectActivate(func(_ *glib.Variant) {
		a.onAddRemoteBackend()
	})
	a.app.AddAction(addRemote)

	prefs := gio.NewSimpleAction("preferences", nil)
	prefs.ConnectActivate(func(_ *glib.Variant) {
		a.onPreferences()
	})
	a.app.AddAction(prefs)

	varsets := gio.NewSimpleAction("variable-sets", nil)
	varsets.ConnectActivate(func(_ *glib.Variant) {
		a.onVariableSets()
	})
	a.app.AddAction(varsets)

	quit := gio.NewSimpleAction("quit", nil)
	quit.ConnectActivate(func(_ *glib.Variant) {
		a.app.Quit()
	})
	a.app.AddAction(quit)

	a.app.SetAccelsForAction("app.quit", []string{"<Primary>q"})
	a.app.SetAccelsForAction("app.preferences", []string{"<Primary>comma"})
	a.app.SetAccelsForAction("app.add-local-project", []string{"<Primary>n"})
	a.app.SetAccelsForAction("app.add-remote-backend", []string{"<Primary><Shift>n"})
	a.app.SetAccelsForAction("app.variable-sets", []string{"<Primary>e"})
}

func (a *App) onActivate() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		cfg = &config.Config{}
	}
	a.cfg = cfg

	// Idempotent migration: any plaintext tokens move to the keyring.
	if n, err := cfg.MigrateTokens(); err != nil {
		slog.Warn("token migration", "err", err)
	} else if n > 0 {
		slog.Info("migrated tokens to keyring", "count", n)
	}

	backends, err := config.BuildBackends(cfg)
	if err != nil {
		slog.Error("build backends", "err", err)
		backends = nil
	}
	a.backends = backends

	// Sweep orphan vars files from runs that didn't shut down cleanly.
	for _, b := range backends {
		if cleaner, ok := b.(interface{ CleanupOrphanArtifacts() }); ok {
			cleaner.CleanupOrphanArtifacts()
		}
	}

	w, err := window.New(a.app, backends, a.locks)
	if err != nil {
		slog.Error("build window", "err", err)
		return
	}
	a.window = w
	w.SetOnRemoveProject(a.removeLocalProject)
	w.SetOnSync(a.syncProject)
	w.SetOnOpenDirectory(a.openProjectDirectory)
	w.SetOnAddSubpath(a.addSubpathFromExisting)
	w.SetOnNewWorkspace(a.newWorkspace)
	w.SetOnDeleteWorkspace(a.deleteWorkspace)
	w.SetOnRefreshWorkspaces(a.refreshWorkspaces)
	w.Present()
	a.refreshAllLocalWorkspacesAsync()
}

// refreshAllLocalWorkspacesAsync fires one goroutine per local project; sidebar updates as results arrive.
func (a *App) refreshAllLocalWorkspacesAsync() {
	for _, b := range a.backends {
		lb, ok := b.(*local.Backend)
		if !ok {
			continue
		}
		for _, p := range a.localProjectIDs(lb) {
			lb := lb
			pid := p
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := lb.RefreshWorkspaces(ctx, pid); err != nil {
					slog.Debug("startup workspace refresh", "project", pid, "err", err)
					return
				}
				glib.IdleAdd(func() bool {
					if a.window != nil {
						if err := a.window.Refresh(a.backends); err != nil {
							slog.Warn("refresh after workspace discovery", "err", err)
						}
					}
					return false
				})
			}()
		}
	}
}

func (a *App) localProjectIDs(b *local.Backend) []string {
	if a.cfg == nil {
		return nil
	}
	var out []string
	for _, bc := range a.cfg.Backends {
		if bc.Type != "local" || bc.ID != b.ID() {
			continue
		}
		for _, p := range bc.Projects {
			out = append(out, p.ID)
		}
	}
	return out
}

func (a *App) removeLocalProject(ws domain.Workspace) {
	if a.cfg == nil || a.window == nil {
		return
	}
	if err := a.cfg.RemoveLocalProject(ws.ProjectID); err != nil {
		slog.Error("remove project", "err", err, "project_id", ws.ProjectID)
		a.window.ToastError("Couldn't remove project: " + err.Error())
		return
	}
	backends, err := config.BuildBackends(a.cfg)
	if err != nil {
		slog.Error("rebuild backends", "err", err)
		a.window.ToastError("Couldn't rebuild backends: " + err.Error())
		return
	}
	a.backends = backends
	if err := a.window.Refresh(backends); err != nil {
		slog.Error("refresh sidebar", "err", err)
	}
	a.window.Toast("Removed " + ws.ProjectName)
}

func (a *App) onAddLocalProject() {
	if a.window == nil {
		return
	}
	dialogs.AddLocal(a.window.GtkWindow(), a.existingLocalClones(), a.completeAddLocal,
		func(onClosed func()) {
			prefs := dialogs.NewPreferences(a.cfg, a.remoteBackends())
			if onClosed != nil {
				prefs.ConnectClosed(onClosed)
			}
			prefs.Present(a.window.GtkWindow())
		})
}

func (a *App) existingLocalClones() []dialogs.ExistingClone {
	if a.cfg == nil {
		return nil
	}
	var out []dialogs.ExistingClone
	for _, bc := range a.cfg.Backends {
		if bc.Type != "local" {
			continue
		}
		for _, p := range bc.Projects {
			out = append(out, dialogs.ExistingClone{GitURL: p.GitURL, GitRef: p.GitRef})
		}
	}
	return out
}

func (a *App) completeAddLocal(p dialogs.LocalProject) {
	if a.cfg == nil {
		cfg, err := config.Load()
		if err != nil {
			slog.Error("load config", "err", err)
			return
		}
		a.cfg = cfg
	}
	pc := config.ProjectConfig{
		Name:        p.Name,
		GitURL:      p.GitURL,
		GitRef:      p.GitRef,
		Subpath:     p.Subpath,
		SSHKeyLabel: p.SSHKeyLabel,
		GitUsername: p.Username,
	}
	_, saved, err := a.cfg.AddLocalProject(pc)
	if err != nil {
		slog.Error("save project", "err", err, "url", p.GitURL)
		if a.window != nil {
			a.window.ToastError("Couldn't save project: " + err.Error())
		}
		return
	}
	a.window.Toast("Cloning " + p.Name + "…")
	go a.cloneAndPublish(saved, p)
}

func (a *App) cloneAndPublish(saved config.ProjectConfig, p dialogs.LocalProject) {
	cloneDir, err := local.CloneDir(saved.GitURL, saved.GitRef)
	if err != nil {
		a.handleCloneFailure(saved, "clone path: "+err.Error())
		return
	}
	auth, err := dialogs.BuildAuth(p)
	if err != nil {
		a.handleCloneFailure(saved, err.Error())
		return
	}
	if _, statErr := os.Stat(cloneDir); statErr == nil {
		a.publishLocalSuccess(saved, p.Name, false)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := gitutils.Clone(ctx, saved.GitURL, saved.GitRef, cloneDir, auth); err != nil {
		a.handleCloneFailure(saved, err.Error())
		return
	}
	a.publishLocalSuccess(saved, p.Name, true)
}

func (a *App) publishLocalSuccess(saved config.ProjectConfig, name string, cloned bool) {
	glib.IdleAdd(func() bool {
		backends, err := config.BuildBackends(a.cfg)
		if err != nil {
			slog.Error("rebuild backends", "err", err)
			if a.window != nil {
				a.window.ToastError("Couldn't rebuild backends: " + err.Error())
			}
			return false
		}
		a.backends = backends
		if a.window != nil {
			if err := a.window.Refresh(backends); err != nil {
				slog.Error("refresh sidebar", "err", err)
			}
			if cloned {
				a.window.Toast("Cloned " + name)
			} else {
				a.window.Toast("Added " + name)
			}
		}
		return false
	})
}

func (a *App) handleCloneFailure(saved config.ProjectConfig, msg string) {
	slog.Error("clone failed", "err", msg, "url", saved.GitURL, "ref", saved.GitRef)
	if remErr := a.cfg.RemoveLocalProject(saved.ID); remErr != nil {
		slog.Warn("rollback after clone failure", "err", remErr)
	}
	glib.IdleAdd(func() bool {
		if a.window != nil {
			a.window.ToastError("Clone failed: " + msg)
		}
		return false
	})
}

func (a *App) onPreferences() {
	if a.window == nil {
		return
	}
	prefs := dialogs.NewPreferences(a.cfg, a.remoteBackends())
	prefs.Present(a.window.GtkWindow())
}

func (a *App) remoteBackends() []dialogs.RemoteBackend {
	var out []dialogs.RemoteBackend
	for _, b := range a.backends {
		if b.Kind() == domain.BackendKindLocal {
			continue
		}
		if rb, ok := b.(dialogs.RemoteBackend); ok {
			out = append(out, rb)
		}
	}
	return out
}

// onVariableSets uses the first local backend (the canonical varset
// store) since global varsets aren't tied to any specific backend.
func (a *App) onVariableSets() {
	if a.window == nil {
		return
	}
	backend, projects := a.firstLocalBackend()
	if backend == nil {
		a.window.ToastError("Add a local project first to manage variable sets")
		return
	}
	dialogs.PresentVarsets(a.window.GtkWindow(), backend, projects, a.localWorkspaces())
}

func (a *App) localWorkspaces() []domain.Workspace {
	var out []domain.Workspace
	for _, b := range a.backends {
		if b.Kind() != domain.BackendKindLocal {
			continue
		}
		ws, err := b.Workspaces(context.Background())
		if err != nil {
			slog.Warn("list workspaces for varsets dialog", "backend", b.ID(), "err", err)
			continue
		}
		out = append(out, ws...)
	}
	return out
}

// firstLocalBackend returns the first registered local backend, the
// only kind that satisfies VarsetsBackend (remote backends have their
// own server-side varsets).
func (a *App) firstLocalBackend() (dialogs.VarsetsBackend, []dialogs.ProjectChoice) {
	for _, b := range a.backends {
		if b.Kind() != domain.BackendKindLocal {
			continue
		}
		vb, ok := b.(dialogs.VarsetsBackend)
		if !ok {
			continue
		}
		var projects []dialogs.ProjectChoice
		if pl, ok := b.(interface {
			ListProjects(context.Context) []domain.ProjectChoice
		}); ok {
			projects = pl.ListProjects(context.Background())
		}
		return vb, projects
	}
	return nil, nil
}

func (a *App) onAddRemoteBackend() {
	if a.window == nil {
		return
	}
	dialogs.AddRemote(a.window.GtkWindow(), a.completeAddRemote)
}

func (a *App) completeAddRemote(form dialogs.RemoteForm) {
	if a.cfg == nil {
		cfg, err := config.Load()
		if err != nil {
			slog.Error("load config", "err", err)
			return
		}
		a.cfg = cfg
	}
	if _, err := a.cfg.AddRemoteBackend(form.Name, string(form.Flavor),
		form.Endpoint, form.Organization, form.Token); err != nil {
		slog.Error("save remote backend", "err", err)
		a.window.ToastError("Couldn't save backend: " + err.Error())
		return
	}
	backends, err := config.BuildBackends(a.cfg)
	if err != nil {
		slog.Error("rebuild backends", "err", err)
		a.window.ToastError("Couldn't rebuild backends: " + err.Error())
		return
	}
	a.backends = backends
	if err := a.window.Refresh(backends); err != nil {
		slog.Error("refresh sidebar", "err", err)
	}
	a.window.Toast("Connected to " + form.Organization)
}

func (a *App) projectFor(ws domain.Workspace) (config.ProjectConfig, bool) {
	if a.cfg == nil {
		return config.ProjectConfig{}, false
	}
	for _, bc := range a.cfg.Backends {
		if bc.Type != "local" || bc.ID != ws.BackendID {
			continue
		}
		for _, p := range bc.Projects {
			if p.ID == ws.ProjectID {
				return p, true
			}
		}
	}
	return config.ProjectConfig{}, false
}

func (a *App) syncProject(ws domain.Workspace) {
	if a.window == nil {
		return
	}
	p, ok := a.projectFor(ws)
	if !ok || p.GitURL == "" {
		a.window.ToastError("Workspace has no associated git repo")
		return
	}
	dlg := adw.NewAlertDialog(
		"Sync from git remote?",
		"This fetches the latest commits and resets the working clone to the remote — any local edits inside the clone are discarded.",
	)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("sync", "Sync")
	dlg.SetResponseAppearance("sync", adw.ResponseSuggested)
	dlg.SetDefaultResponse("sync")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp != "sync" {
			return
		}
		go a.runSync(ws, p)
	})
	dlg.Present(a.window.GtkWindow())
}

func (a *App) runSync(ws domain.Workspace, p config.ProjectConfig) {
	cloneDir, err := local.CloneDir(p.GitURL, p.GitRef)
	if err != nil {
		a.toastFromGoroutine("Sync failed: " + err.Error())
		return
	}
	auth, err := dialogs.BuildAuth(dialogs.LocalProject{
		GitURL:      p.GitURL,
		Username:    p.GitUsername,
		SSHKeyLabel: p.SSHKeyLabel,
	})
	if err != nil {
		a.toastFromGoroutine("Sync failed: " + err.Error())
		return
	}
	glib.IdleAdd(func() bool {
		if a.window != nil {
			a.window.SetWorkspacePageSyncBusy(true)
		}
		return false
	})
	defer glib.IdleAdd(func() bool {
		if a.window != nil {
			a.window.SetWorkspacePageSyncBusy(false)
		}
		return false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := gitutils.Sync(ctx, cloneDir, p.GitRef, auth); err != nil {
		slog.Error("git sync", "err", err, "ws", ws.ID)
		a.toastFromGoroutine("Sync failed: " + err.Error())
		return
	}
	a.refreshWorkspacesNow(ws)
	a.toastFromGoroutine("Synced " + p.Name)
}

func (a *App) openProjectDirectory(ws domain.Workspace) {
	if a.window == nil {
		return
	}
	dir := ws.WorkingDirectory
	if dir == "" {
		a.window.ToastError("Workspace has no working directory")
		return
	}
	launcher := gtk.NewFileLauncher(gio.NewFileForPath(dir))
	launcher.Launch(context.Background(), a.window.GtkWindow(), func(res gio.AsyncResulter) {
		if err := launcher.LaunchFinish(res); err != nil {
			slog.Warn("open workspace directory", "err", err, "dir", dir)
			if a.window != nil {
				a.window.ToastError("Couldn't open directory: " + err.Error())
			}
		}
	})
}

func (a *App) addSubpathFromExisting(ws domain.Workspace) {
	if a.window == nil {
		return
	}
	src, ok := a.projectFor(ws)
	if !ok || src.GitURL == "" {
		return
	}
	dialogs.AddSubpathFor(a.window.GtkWindow(), dialogs.ProjectSource{
		Name:        src.Name,
		GitURL:      src.GitURL,
		GitRef:      src.GitRef,
		GitUsername: src.GitUsername,
		SSHKeyLabel: src.SSHKeyLabel,
	}, a.completeAddLocal)
}

func (a *App) toastFromGoroutine(msg string) {
	glib.IdleAdd(func() bool {
		if a.window != nil {
			a.window.Toast(msg)
		}
		return false
	})
}

func (a *App) localBackendFor(backendID string) *local.Backend {
	for _, b := range a.backends {
		if b.ID() != backendID {
			continue
		}
		if lb, ok := b.(*local.Backend); ok {
			return lb
		}
	}
	return nil
}

func (a *App) newWorkspace(ws domain.Workspace) {
	if a.window == nil {
		return
	}
	lb := a.localBackendFor(ws.BackendID)
	if lb == nil {
		return
	}
	dialogs.PresentNewWorkspace(a.window.GtkWindow(), ws.ProjectName, func(name string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := lb.CreateTofuWorkspace(ctx, ws.ProjectID, name); err != nil {
				slog.Error("create workspace", "err", err, "project", ws.ProjectID, "name", name)
				a.toastErrorFromGoroutine("Couldn't create workspace: " + err.Error())
				return
			}
			if err := lb.RefreshWorkspaces(ctx, ws.ProjectID); err != nil {
				slog.Warn("post-create refresh", "err", err)
			}
			a.refreshSidebarFromGoroutine()
			a.toastFromGoroutine("Created workspace " + name)
		}()
	})
}

func (a *App) deleteWorkspace(ws domain.Workspace) {
	lb := a.localBackendFor(ws.BackendID)
	if lb == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := lb.DeleteTofuWorkspace(ctx, ws.ProjectID, ws.Name); err != nil {
			slog.Error("delete workspace", "err", err, "ws", ws.ID)
			a.toastErrorFromGoroutine("Couldn't delete workspace: " + err.Error())
			return
		}
		if err := lb.RefreshWorkspaces(ctx, ws.ProjectID); err != nil {
			slog.Warn("post-delete refresh", "err", err)
		}
		a.refreshSidebarFromGoroutine()
		a.toastFromGoroutine("Deleted workspace " + ws.Name)
	}()
}

func (a *App) refreshWorkspaces(ws domain.Workspace) {
	a.refreshWorkspacesNow(ws)
}

func (a *App) refreshWorkspacesNow(ws domain.Workspace) {
	lb := a.localBackendFor(ws.BackendID)
	if lb == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lb.RefreshWorkspaces(ctx, ws.ProjectID); err != nil {
			slog.Warn("manual workspace refresh", "err", err)
			a.toastErrorFromGoroutine("Refresh failed: " + err.Error())
			return
		}
		a.refreshSidebarFromGoroutine()
	}()
}

func (a *App) refreshSidebarFromGoroutine() {
	glib.IdleAdd(func() bool {
		if a.window != nil {
			if err := a.window.Refresh(a.backends); err != nil {
				slog.Warn("refresh sidebar", "err", err)
			}
		}
		return false
	})
}

func (a *App) toastErrorFromGoroutine(msg string) {
	glib.IdleAdd(func() bool {
		if a.window != nil {
			a.window.ToastError(msg)
		}
		return false
	})
}
