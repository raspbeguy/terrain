// Package ui hosts the GTK-side of Terrain. The boundary rule: this package
// (and its subpackages) is the only place allowed to import gotk4. Domain
// and backend code stays headless.
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

// AppID is the reverse-DNS application identifier registered with the
// session bus and matched against the .desktop file.
const AppID = "io.github.raspbeguy.Terrain"

// App owns the AdwApplication and the live state derived from the on-disk
// config (backends, window). One App per process.
type App struct {
	app *adw.Application

	cfg      *config.Config
	backends []domain.Backend
	window   *window.Window
	locks    *runner.WorkspaceLocks
}

// NewApp constructs the application but does not run the main loop. Call
// Run to start.
func NewApp() *App {
	return &App{
		app:   adw.NewApplication(AppID, gio.ApplicationFlagsNone),
		locks: runner.NewWorkspaceLocks(),
	}
}

// Run blocks until the application exits. Returns the GApplication exit code.
func (a *App) Run(args []string) int {
	a.app.ConnectActivate(a.onActivate)
	a.registerActions()
	return a.app.Run(args)
}

// registerActions wires the global "app." actions referenced from the
// Blueprint definitions. Activate handlers may run before onActivate (e.g.
// from the command line via `gapplication action`) so they must not assume
// the window exists.
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

	// Keyboard shortcuts. <Primary> is Ctrl on Linux/X, Cmd on macOS — gtk
	// resolves it per platform. We bind the basics that have analogues in
	// every GNOME app; per-tab shortcuts (F5 to refresh active tab, etc.)
	// can come once we have a focus tracker.
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
		// Fall through with defaults — first-run state is fine without a config
		cfg = &config.Config{}
	}
	a.cfg = cfg

	// One-shot migration: if any backend still has a plaintext token, move
	// it into the keyring. Idempotent — no-op when nothing to do.
	if n, err := cfg.MigrateTokens(); err != nil {
		slog.Warn("token migration", "err", err)
	} else if n > 0 {
		slog.Info("migrated tokens to keyring", "count", n)
	}

	backends, err := config.BuildBackends(cfg)
	if err != nil {
		slog.Error("build backends", "err", err)
		// Continue with empty backend list; user sees first-run state
		backends = nil
	}
	a.backends = backends

	// Best-effort cleanup of orphan vars.auto.tfvars.json files left from
	// runs that didn't shut down cleanly. The local backend is the only
	// kind that writes them; we type-assert to the interface so this stays
	// future-compatible if other backends ever do similar.
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
	w.Present()
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

// completeAddLocal persists the picked project to the config and refreshes
// the sidebar. Runs synchronously on the GTK main thread because it's the
// continuation of a FileDialog callback (already main-thread).
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

// remoteBackends returns the registered remote backends adapted to the
// dialogs.RemoteBackend interface. Local backends are filtered out — they
// have no API to probe.
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

// onVariableSets opens the Variable Sets management dialog. We pick the
// first local backend in the registry — global varsets aren't tied to any
// specific local backend, but the dialog needs *some* backend for the CRUD
// operations (the local backend's varset storage is the canonical store).
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

// localWorkspaces returns the flat list of all local workspaces across the
// registered backends. Used by the variable-sets dialog to populate the
// workspace-scope attachment list.
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

// firstLocalBackend returns the first registered local backend cast to the
// VarsetsBackend interface, plus its registered projects. Variable sets are
// local-only for now; remote backends (TFE/HCP/OTF) have their own server-
// side varsets.
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

// completeAddRemote persists the new remote backend and refreshes the
// sidebar. Listing the remote workspaces happens lazily on the first sidebar
// row click — the API call could take seconds on a large org.
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
