// Package ui hosts the GTK-side of terrain. Boundary rule: ui (and
// subpackages) is the only place allowed to import gotk4.
package ui

import (
	"context"
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"github.com/raspbeguy/terrain/internal/config"
	"github.com/raspbeguy/terrain/internal/domain"
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
	w.Present()
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
	dialogs.AddLocal(
		context.Background(),
		a.window.GtkWindow(),
		a.completeAddLocal,
		func(err error) { slog.Error("add local project", "err", err) },
	)
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
	if _, _, err := a.cfg.AddLocalProject(p.Name, p.Path); err != nil {
		slog.Error("save project", "err", err, "path", p.Path)
		if a.window != nil {
			a.window.ToastError("Couldn't save project: " + err.Error())
		}
		return
	}
	backends, err := config.BuildBackends(a.cfg)
	if err != nil {
		slog.Error("rebuild backends", "err", err)
		if a.window != nil {
			a.window.ToastError("Couldn't rebuild backends: " + err.Error())
		}
		return
	}
	a.backends = backends
	if a.window != nil {
		if err := a.window.Refresh(backends); err != nil {
			slog.Error("refresh sidebar", "err", err)
		}
		a.window.Toast("Added " + p.Name)
	}
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
