// Package window owns the main AdwApplicationWindow and its sidebar. The
// window is loaded from the embedded gresource bundle (window.ui, compiled
// from window.blp by the meson pipeline).
package window

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/runner"
	"github.com/raspbeguy/terrain/internal/ui/bridge"
	"github.com/raspbeguy/terrain/internal/ui/dialogs"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
	"github.com/raspbeguy/terrain/internal/ui/views/run"
	"github.com/raspbeguy/terrain/internal/ui/views/workspace"
)

const uiResource = "/io/github/raspbeguy/Terrain/window.ui"

// Window wraps the gresource-loaded AdwApplicationWindow with the live
// references we mutate from Go (sidebar list, content stack, etc.).
type Window struct {
	app      *adw.Application
	backends []domain.Backend
	locks    *runner.WorkspaceLocks

	root          *adw.ApplicationWindow
	toastOverlay  *adw.ToastOverlay
	sidebarStack  *gtk.Stack
	sidebarList   *gtk.ListBox
	contentStack  *gtk.Stack
	contentTitle  *adw.WindowTitle
	workspaceBin  *adw.Bin
	workspacePage *workspace.Page
	runPage       *run.Page

	// Flat list of workspaces in row order; index matches ListBoxRow.Index().
	workspaces []domain.Workspace

	// Currently displayed workspace, used to restore content when a run
	// finishes or the user clicks Back.
	current domain.Workspace

	// Last produced plan path, captured between Plan and Apply.
	lastPlanFile string

	// onRemoveProject, if set by the application, is invoked after the user
	// confirms the "Remove project" action on a sidebar row. The receiver is
	// expected to update config + rebuild backends + call Refresh.
	onRemoveProject func(domain.Workspace)
}

// SetOnRemoveProject installs the callback fired when the user confirms
// removing a project from a sidebar row's kebab menu.
func (w *Window) SetOnRemoveProject(fn func(domain.Workspace)) {
	w.onRemoveProject = fn
}

// New loads the window from gresource, wires it to the given application,
// and populates the sidebar from the provided backends. locks is the
// per-workspace lock registry — pass the same instance the app uses so the
// rest of the UI sees consistent state.
func New(app *adw.Application, backends []domain.Backend, locks *runner.WorkspaceLocks) (*Window, error) {
	if locks == nil {
		locks = runner.NewWorkspaceLocks()
	}
	builder := gtk.NewBuilderFromResource(uiResource)

	w := &Window{
		app:           app,
		backends:      backends,
		locks:         locks,
		root:          uihelpers.MustCast[*adw.ApplicationWindow](builder, "main_window"),
		toastOverlay:  uihelpers.MustCast[*adw.ToastOverlay](builder, "main_toast_overlay"),
		sidebarStack:  uihelpers.MustCast[*gtk.Stack](builder, "sidebar_stack"),
		sidebarList:   uihelpers.MustCast[*gtk.ListBox](builder, "sidebar_listbox"),
		contentStack:  uihelpers.MustCast[*gtk.Stack](builder, "content_stack"),
		contentTitle:  uihelpers.MustCast[*adw.WindowTitle](builder, "content_title"),
		workspaceBin:  uihelpers.MustCast[*adw.Bin](builder, "workspace_detail_container"),
		workspacePage: workspace.New(),
		runPage:       run.New(),
	}
	w.root.SetApplication(&app.Application)
	w.workspaceBin.SetChild(w.workspacePage.Root())
	w.workspacePage.SetOnNewPlan(w.startPlan)
	w.workspacePage.SetOnLoadState(w.loadState)
	w.workspacePage.SetOnLoadStateVersions(w.loadStateVersions)
	w.workspacePage.SetOnLoadStateVersion(w.loadStateVersion)
	w.workspacePage.SetOnCompareStates(w.compareStates)
	w.workspacePage.SetOnLoadRuns(w.loadRuns)
	w.workspacePage.SetOnLoadVariables(w.loadVariables)
	w.workspacePage.SetOnOpenRun(w.openRun)
	w.workspacePage.SetOnEditVariable(w.editVariable)
	w.workspacePage.SetOnAddVariable(w.addVariable)
	w.workspacePage.SetOnRemoveVariable(w.removeVariable)
	w.workspacePage.SetOnOpenSettings(w.openWorkspaceSettings)
	w.runPage.SetOnBack(w.showWorkspaceView)
	w.runPage.SetOnStatus(func(status domain.RunStatus, _ string) {
		w.contentTitle.SetSubtitle(string(status))
	})

	w.sidebarList.ConnectRowActivated(w.onRowActivated)
	w.sidebarList.SetHeaderFunc(w.sidebarHeaderFunc)

	if err := w.refreshFrom(backends); err != nil {
		slog.Warn("sidebar populate", "err", err)
	}

	slog.Info("window built",
		"backends", len(backends),
		"workspaces", len(w.workspaces),
	)
	return w, nil
}

// Present shows the window. Mirrors AdwApplicationWindow.Present but exposes
// it through the wrapper so callers don't need to reach into root.
func (w *Window) Present() { w.root.Present() }

// GtkWindow returns the underlying *gtk.Window — used to parent transient
// dialogs (GtkFileDialog, AdwAlertDialog, etc.).
func (w *Window) GtkWindow() *gtk.Window { return &w.root.Window }

// Toast surfaces a transient message in the main window. Safe to call from
// the GTK main thread; route through bridge.PumpRun if a domain goroutine
// needs to toast.
func (w *Window) Toast(message string) {
	if w.toastOverlay == nil {
		return
	}
	t := adw.NewToast(message)
	t.SetTimeout(4)
	w.toastOverlay.AddToast(t)
}

// ToastError is a Toast variant marked for the error styling (longer
// timeout, no priority for the success-style fade).
func (w *Window) ToastError(message string) {
	if w.toastOverlay == nil {
		return
	}
	t := adw.NewToast(message)
	t.SetTimeout(8)
	t.SetPriority(adw.ToastPriorityHigh)
	w.toastOverlay.AddToast(t)
}

// Refresh reloads the sidebar from a fresh backend list — call after
// Add Local Project completes, etc.
func (w *Window) Refresh(backends []domain.Backend) error {
	return w.refreshFrom(backends)
}

// refreshFrom populates the sidebar from the supplied backend list. Local
// backends (filesystem-only) are read synchronously so their rows show
// immediately; remote backends are read on background goroutines and post
// their workspaces back via bridge.OnMainThread, appending to the sidebar
// when they arrive. This keeps window startup ms-fast even when an OTF /
// HCP / TFE org has thousands of workspaces — the user gets an interactive
// UI right away and remote rows fill in shortly after.
func (w *Window) refreshFrom(backends []domain.Backend) error {
	ctx := context.Background()
	w.backends = backends

	var local []domain.Workspace
	for _, b := range backends {
		if b.Kind() != domain.BackendKindLocal {
			continue
		}
		list, err := b.Workspaces(ctx)
		if err != nil {
			return fmt.Errorf("backend %q workspaces: %w", b.ID(), err)
		}
		local = append(local, list...)
	}
	w.workspaces = local
	w.rebuildSidebar()

	for _, b := range backends {
		if b.Kind() == domain.BackendKindLocal {
			continue
		}
		go w.fetchRemoteWorkspaces(b)
	}
	return nil
}

// fetchRemoteWorkspaces is the background-goroutine half of remote workspace
// loading. Bounded by a 30s timeout so a stuck API doesn't leave the
// goroutine hanging forever; an error there surfaces as a toast on the
// main thread.
func (w *Window) fetchRemoteWorkspaces(b domain.Backend) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := b.Workspaces(ctx)
	bridge.OnMainThread(func() {
		if err != nil {
			slog.Warn("fetch remote workspaces", "backend", b.ID(), "err", err)
			w.ToastError("Couldn't load workspaces from " + b.DisplayName() + ": " + err.Error())
			return
		}
		slog.Info("remote workspaces loaded", "backend", b.ID(), "count", len(list))
		w.replaceBackendWorkspaces(b.ID(), list)
	})
}

// replaceBackendWorkspaces drops any workspaces currently attributed to
// backendID and replaces them with list, rebuilding the sidebar so row
// order matches the flat workspaces slice. Used by fetchRemoteWorkspaces
// to merge async-arriving remote rows into the sidebar without disturbing
// the local rows already shown.
func (w *Window) replaceBackendWorkspaces(backendID string, list []domain.Workspace) {
	next := make([]domain.Workspace, 0, len(w.workspaces)+len(list))
	for _, ws := range w.workspaces {
		if ws.BackendID != backendID {
			next = append(next, ws)
		}
	}
	next = append(next, list...)
	w.workspaces = next
	w.rebuildSidebar()
}

// rebuildSidebar wipes and re-creates all sidebar rows from w.workspaces.
// Cheap enough to run on every list-shape change (a few thousand widget
// ops max); a future virtualized list view would let us skip the wipe.
func (w *Window) rebuildSidebar() {
	w.clearList()

	if len(w.workspaces) == 0 {
		w.sidebarStack.SetVisibleChildName("empty")
		w.contentStack.SetVisibleChildName("welcome")
		return
	}

	w.sidebarStack.SetVisibleChildName("list")
	for _, ws := range w.workspaces {
		row := adw.NewActionRow()
		row.SetTitle(ws.ProjectName)
		row.SetSubtitle(ws.Name)
		row.SetActivatable(true)
		w.attachRowKebab(row, ws)
		w.sidebarList.Append(row)
	}

	// If the active workspace was removed in this refresh, fall back to the
	// welcome view so we don't leave a stale detail pane bound to a workspace
	// that no longer exists.
	if w.current.ID != "" {
		stillThere := false
		for _, ws := range w.workspaces {
			if ws.ID == w.current.ID {
				stillThere = true
				break
			}
		}
		if !stillThere {
			w.current = domain.Workspace{}
			w.contentStack.SetVisibleChildName("welcome")
			w.contentTitle.SetTitle("Terrain")
			w.contentTitle.SetSubtitle("")
		}
	}
}

// attachRowKebab adds a kebab menu (⋯) suffix to a sidebar row with one
// "Remove project" action. Local-only — remote workspaces aren't directly
// removable from the sidebar (you'd remove the whole remote backend).
func (w *Window) attachRowKebab(row *adw.ActionRow, ws domain.Workspace) {
	if ws.BackendID == "" || !isLocalBackendID(w.backends, ws.BackendID) {
		return
	}

	popover := gtk.NewPopover()
	popover.SetHasArrow(true)

	removeBtn := gtk.NewButtonWithLabel("Remove project")
	removeBtn.AddCSSClass("flat")
	removeBtn.AddCSSClass("destructive-action")
	removeBtn.ConnectClicked(func() {
		popover.Popdown()
		w.confirmRemoveProject(ws)
	})
	popover.SetChild(removeBtn)

	menu := gtk.NewMenuButton()
	menu.SetIconName("view-more-symbolic")
	menu.AddCSSClass("flat")
	menu.SetVAlign(gtk.AlignCenter)
	menu.SetPopover(popover)
	menu.SetTooltipText("More actions")

	row.AddSuffix(menu)
}

// isLocalBackendID reports whether the backend with id is a local backend.
// Used to gate per-row UI affordances that only apply to local workspaces.
func isLocalBackendID(backends []domain.Backend, id string) bool {
	for _, b := range backends {
		if b.ID() == id {
			return b.Kind() == domain.BackendKindLocal
		}
	}
	return false
}

// confirmRemoveProject opens an AdwAlertDialog asking the user to confirm
// before unregistering ws from the local backend. On confirmation, fires
// the OnRemoveProject callback installed by the App.
func (w *Window) confirmRemoveProject(ws domain.Workspace) {
	dlg := adw.NewAlertDialog(
		"Remove project?",
		fmt.Sprintf("This unregisters %q from terrain. The project files on disk are not deleted.", ws.ProjectName),
	)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("remove", "Remove")
	dlg.SetResponseAppearance("remove", adw.ResponseDestructive)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp == "remove" && w.onRemoveProject != nil {
			w.onRemoveProject(ws)
		}
	})
	dlg.Present(&w.root.Window)
}

func (w *Window) clearList() {
	for {
		row := w.sidebarList.RowAtIndex(0)
		if row == nil {
			return
		}
		w.sidebarList.Remove(row)
	}
}

// sidebarHeaderFunc inserts a section heading above the first workspace of
// each backend. GtkListBox calls this on rebuild and when adjacent rows
// change.
func (w *Window) sidebarHeaderFunc(row, before *gtk.ListBoxRow) {
	idx := row.Index()
	if idx < 0 || int(idx) >= len(w.workspaces) {
		row.SetHeader(nil)
		return
	}
	currentBackend := w.workspaces[idx].BackendID

	if before != nil {
		prevIdx := before.Index()
		if prevIdx >= 0 && int(prevIdx) < len(w.workspaces) &&
			w.workspaces[prevIdx].BackendID == currentBackend {
			row.SetHeader(nil)
			return
		}
	}

	backend := w.findBackend(currentBackend)
	name := currentBackend
	if backend != nil {
		name = backend.DisplayName()
	}

	label := gtk.NewLabel(name)
	label.AddCSSClass("heading")
	label.AddCSSClass("dim-label")
	label.SetXAlign(0)
	label.SetMarginTop(8)
	label.SetMarginStart(12)
	label.SetMarginEnd(12)
	label.SetMarginBottom(4)
	row.SetHeader(label)
}

func (w *Window) onRowActivated(row *gtk.ListBoxRow) {
	idx := row.Index()
	if idx < 0 || int(idx) >= len(w.workspaces) {
		return
	}
	ws := w.workspaces[idx]
	slog.Info("workspace activated", "id", ws.ID, "project", ws.ProjectName, "name", ws.Name)

	w.current = ws
	w.contentTitle.SetTitle(ws.ProjectName)
	w.contentTitle.SetSubtitle(ws.Name)

	w.workspacePage.Bind(ws)
	w.workspaceBin.SetChild(w.workspacePage.Root())
	w.contentStack.SetVisibleChildName("workspace")
}

// startPlan is the callback fired by the workspace page's "New Plan" button.
// Resolves the backend, acquires the per-workspace lock (refusing
// concurrent runs on the same workspace), kicks StartRun, swaps to the run
// detail view, pumps events, releases the lock when the stream closes.
func (w *Window) startPlan(ws domain.Workspace) {
	slog.Info("start plan", "ws", ws.ID, "backend", ws.BackendID)
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		slog.Error("backend not found", "id", ws.BackendID)
		w.ToastError("Backend not found")
		return
	}

	release, ok := w.locks.TryAcquire(ws.ID)
	if !ok {
		w.ToastError("A run is already in progress for this workspace")
		return
	}

	req := domain.RunRequest{
		WorkspaceID: ws.ID,
		Kind:        domain.RunKindPlan,
		Message:     "manual run",
	}

	r, stream, cancel, err := backend.StartRun(context.Background(), req)
	if err != nil {
		release()
		slog.Error("StartRun", "err", err)
		w.ToastError("Couldn't start plan: " + err.Error())
		return
	}
	slog.Info("plan run started", "id", r.ID, "kind", r.Kind)
	w.Toast("Plan started")
	go func() {
		<-stream.Done()
		release()
	}()

	w.workspaceBin.SetChild(w.runPage.Root())
	w.contentTitle.SetTitle(ws.ProjectName + " · " + ws.Name)
	w.runPage.Start(r, stream, cancel,
		func(plan *domain.PlanResult) { w.startApply(ws, plan) },
		w.showWorkspaceView,
	)
}

// startApply consumes a successful plan's file path and starts a new apply
// run that consumes it. The apply binds to the same run page, replacing the
// plan's stream.
func (w *Window) startApply(ws domain.Workspace, plan *domain.PlanResult) {
	if plan == nil || plan.File == "" {
		slog.Warn("apply skipped: no plan file")
		return
	}
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		slog.Error("backend not found for apply", "id", ws.BackendID)
		return
	}
	req := domain.RunRequest{
		WorkspaceID: ws.ID,
		Kind:        domain.RunKindApply,
		PlanFile:    plan.File,
		ParentRunID: plan.RunID,
		Message:     "applying plan",
	}
	release, ok := w.locks.TryAcquire(ws.ID)
	if !ok {
		w.ToastError("A run is already in progress for this workspace")
		return
	}
	slog.Info("start apply", "ws", ws.ID, "plan", plan.File)
	r, stream, cancel, err := backend.StartRun(context.Background(), req)
	if err != nil {
		release()
		slog.Error("apply StartRun", "err", err)
		w.ToastError("Couldn't start apply: " + err.Error())
		return
	}
	slog.Info("apply run started", "id", r.ID)
	w.Toast("Applying plan")
	go func() {
		<-stream.Done()
		release()
	}()
	w.runPage.Start(r, stream, cancel,
		nil, // applies don't produce a follow-up apply
		w.showWorkspaceView,
	)
}

func (w *Window) showWorkspaceView() {
	if w.current.ID == "" {
		w.contentStack.SetVisibleChildName("welcome")
		return
	}
	w.workspacePage.Bind(w.current)
	w.workspaceBin.SetChild(w.workspacePage.Root())
	w.contentTitle.SetTitle(w.current.ProjectName)
	w.contentTitle.SetSubtitle(w.current.Name)
}

func (w *Window) findBackend(id string) domain.Backend {
	for _, b := range w.backends {
		if b.ID() == id {
			return b
		}
	}
	return nil
}

// stateLoader is the optional capability the local backend exposes for the
// State tab. Remote backends will satisfy it via go-tfe in M4.
type stateLoader interface {
	LoadState(ctx context.Context, workspaceID string) (*tfjson.State, error)
}

// loadState bridges the workspace State tab refresh button to the backend's
// state-loading capability. Returns a clean error if the backend doesn't
// support state introspection so the UI can show a helpful message.
//
// Acquires the per-workspace lock non-blocking: a `tofu show -json` call
// contends with an in-flight `tofu plan/apply` for the state-lock file.
// If we just block, the UI freezes. If we just call show-json regardless,
// terraform errors out with "Error acquiring the state lock" which is also
// fine but harder to localize. TryAcquire + bail with a clear message is
// the kindest path.
func (w *Window) loadState(ws domain.Workspace) (*tfjson.State, error) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		return nil, fmt.Errorf("backend %q not found", ws.BackendID)
	}
	loader, ok := backend.(stateLoader)
	if !ok {
		return nil, errors.New("backend does not support state inspection yet")
	}

	release, ok := w.locks.TryAcquire(ws.ID)
	if !ok {
		return nil, errors.New("a run is in progress; refresh state once it finishes")
	}
	defer release()

	return loader.LoadState(context.Background(), ws.ID)
}

// stateVersionLister is the optional capability for backends that persist
// state-version snapshots. Local satisfies it via the snapshot directory;
// remote backends can satisfy it via TFE's state-versions API in a future
// session.
type stateVersionLister interface {
	StateVersions(ctx context.Context, workspaceID string) ([]domain.StateVersion, error)
	LoadStateVersion(ctx context.Context, workspaceID, versionID string) (*tfjson.State, error)
}

// loadStateVersions returns the snapshot list plus a lineage warning if
// the most recent snapshot has a different lineage from the previous one.
// Returns (nil, nil, nil) cleanly when the backend doesn't satisfy the
// optional interface — the UI just shows "Live" with no snapshots.
func (w *Window) loadStateVersions(ws domain.Workspace) ([]domain.StateVersion, *workspace.LineageWarning, error) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		return nil, nil, fmt.Errorf("backend %q not found", ws.BackendID)
	}
	lister, ok := backend.(stateVersionLister)
	if !ok {
		return nil, nil, nil
	}
	versions, err := lister.StateVersions(context.Background(), ws.ID)
	if err != nil {
		return nil, nil, err
	}
	return versions, lineageWarn(versions), nil
}

// lineageWarn detects a lineage change between the most recent snapshot
// and the one before it (StateVersions returns newest-first). Returns
// nil when there's no change or fewer than two snapshots.
func lineageWarn(versions []domain.StateVersion) *workspace.LineageWarning {
	if len(versions) < 2 {
		return nil
	}
	if versions[0].Lineage == "" || versions[1].Lineage == "" {
		return nil
	}
	if versions[0].Lineage == versions[1].Lineage {
		return nil
	}
	return &workspace.LineageWarning{
		From: versions[1].Lineage,
		To:   versions[0].Lineage,
	}
}

// loadStateVersion bridges the version-picker callback to the backend's
// LoadStateVersion. Returns the parsed state JSON for one specific
// snapshot.
func (w *Window) loadStateVersion(ws domain.Workspace, versionID string) (*tfjson.State, error) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		return nil, fmt.Errorf("backend %q not found", ws.BackendID)
	}
	lister, ok := backend.(stateVersionLister)
	if !ok {
		return nil, errors.New("backend does not expose state-version history")
	}
	return lister.LoadStateVersion(context.Background(), ws.ID, versionID)
}

// compareStates opens the diff dialog with the workspace's available
// versions. The loader closure routes to LoadState (for "Live") or
// LoadStateVersion (for a specific snapshot ID).
func (w *Window) compareStates(ws domain.Workspace, versions []domain.StateVersion) {
	loader := func(versionID string) (*tfjson.State, error) {
		if versionID == "" {
			return w.loadState(ws)
		}
		return w.loadStateVersion(ws, versionID)
	}
	dialogs.PresentStateDiff(w.GtkWindow(), versions, loader)
}

// runListing is the optional capability for backends that can return past
// runs from local persistence (or, for remote backends, the API).
type runListing interface {
	Runs(ctx context.Context, workspaceID string) ([]domain.Run, error)
}

func (w *Window) loadRuns(ws domain.Workspace) ([]domain.Run, error) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		return nil, fmt.Errorf("backend %q not found", ws.BackendID)
	}
	listing, ok := backend.(runListing)
	if !ok {
		return nil, nil // empty list is fine; backend just doesn't support history
	}
	return listing.Runs(context.Background(), ws.ID)
}

// openRun navigates to the run detail page for a historical run, loading
// log + plan artifacts from disk read-only. No live stream is bound; the
// action buttons stay hidden until the user kicks a new run.
func (w *Window) openRun(ws domain.Workspace, r domain.Run) {
	slog.Info("open historical run", "id", r.ID, "kind", r.Kind, "status", r.Status, "ws", ws.ID)
	w.workspaceBin.SetChild(w.runPage.Root())
	w.contentTitle.SetTitle(ws.ProjectName + " · " + ws.Name)
	w.runPage.LoadHistory(r)
}

// variableLoader is the optional capability for backends that can return
// the variables of a workspace. Local satisfies it via hcl + libsecret;
// remote satisfies it via go-tfe.
type variableLoader interface {
	VariablesForWorkspace(ctx context.Context, workspaceID string) ([]domain.Variable, error)
}

// loadVariables fetches the workspace's variables. Returns an empty slice
// when the backend doesn't implement the capability rather than an error,
// so the Variables tab shows the empty placeholder cleanly.
func (w *Window) loadVariables(ws domain.Workspace) ([]domain.Variable, error) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		return nil, fmt.Errorf("backend %q not found", ws.BackendID)
	}
	loader, ok := backend.(variableLoader)
	if !ok {
		return nil, nil
	}
	return loader.VariablesForWorkspace(context.Background(), ws.ID)
}

// variableUpserter is the optional capability for backends that can save a
// variable. Local backends implement it; remote support lands when go-tfe's
// Variables.Create/Update is wired.
type variableUpserter interface {
	UpsertVariable(ctx context.Context, workspaceID string, v domain.Variable) error
}

// openWorkspaceSettings shows the per-workspace run-mode + image dialog.
// Local-only — remote workspaces don't have a runtime knob (TFE handles
// execution mode server-side). For remote, the gear button in the
// overview is currently a no-op; we silently return rather than show an
// empty dialog.
func (w *Window) openWorkspaceSettings(ws domain.Workspace) {
	if !isLocalBackendID(w.backends, ws.BackendID) {
		w.Toast("Workspace settings are local-backend only")
		return
	}
	dlg := dialogs.NewWorkspaceSettings(ws.BackendID, ws.ID)
	dlg.Present(w.GtkWindow())
}

// addVariable opens the Add Variable dialog for the workspace.
func (w *Window) addVariable(ws domain.Workspace) {
	dialogs.EditVariable(w.GtkWindow(), dialogs.VarEditAdd, domain.Variable{},
		func(v domain.Variable) { w.saveVariable(ws, v) })
}

// editVariable opens the dialog with an existing variable's values.
func (w *Window) editVariable(ws domain.Workspace, v domain.Variable) {
	dialogs.EditVariable(w.GtkWindow(), dialogs.VarEditEdit, v,
		func(saved domain.Variable) { w.saveVariable(ws, saved) })
}

// variableDeleter is the optional capability for backends that support
// removing a workspace variable. Local backends implement it; remote will
// follow when go-tfe's Variables.Delete is wired.
type variableDeleter interface {
	DeleteVariable(ctx context.Context, workspaceID, key string) error
}

// removeVariable opens an AdwAlertDialog asking the user to confirm before
// dropping the variable. Wording adapts to whether the variable is declared
// in source: declared vars fall back to source defaults, ad-hoc ones are
// fully removed from terraform.tfvars / keyring.
func (w *Window) removeVariable(ws domain.Workspace, v domain.Variable) {
	title := "Remove variable?"
	body := fmt.Sprintf("Remove %q from this workspace? It will be deleted from terraform.tfvars and any keyring entries.", v.Key)
	confirmLabel := "Remove"
	if v.Declared {
		title = "Reset variable?"
		body = fmt.Sprintf("Reset %q to its source default? terrain's override is removed; the `variable` block in your .tf files is left untouched.", v.Key)
		confirmLabel = "Reset"
	}
	dlg := adw.NewAlertDialog(title, body)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("confirm", confirmLabel)
	dlg.SetResponseAppearance("confirm", adw.ResponseDestructive)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp == "confirm" {
			w.deleteVariable(ws, v)
		}
	})
	dlg.Present(&w.root.Window)
}

// deleteVariable forwards the actual delete to the backend (if it implements
// variableDeleter) and refreshes the Variables tab on success. Mirrors
// saveVariable's locking discipline: per-workspace lock acquired so an
// in-flight run doesn't materialise vars mid-edit.
func (w *Window) deleteVariable(ws domain.Workspace, v domain.Variable) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		slog.Error("delete variable: backend not found", "id", ws.BackendID)
		return
	}
	deleter, ok := backend.(variableDeleter)
	if !ok {
		slog.Warn("backend does not support variable delete", "id", ws.BackendID)
		w.ToastError("This backend doesn't support removing variables yet")
		return
	}
	release := w.locks.Acquire(ws.ID)
	defer release()
	if err := deleter.DeleteVariable(context.Background(), ws.ID, v.Key); err != nil {
		slog.Error("delete variable", "ws", ws.ID, "key", v.Key, "err", err)
		w.ToastError("Couldn't remove " + v.Key + ": " + err.Error())
		return
	}
	slog.Info("variable removed", "ws", ws.ID, "key", v.Key, "declared", v.Declared)
	w.Toast("Removed " + v.Key)
	w.workspacePage.RefreshVariables()
}

// saveVariable forwards the dialog's payload to the backend (if it
// implements variableUpserter) and refreshes the Variables tab on success.
//
// Acquires the per-workspace lock for the duration of the write so a run
// in flight doesn't materialise variables mid-edit (`hclwrite` does a
// read-modify-write on terraform.tfvars). Acquire blocks; the typical
// hclwrite round-trip is sub-millisecond, but if a run is reading the
// file it'll wait for materialise to finish — acceptable.
func (w *Window) saveVariable(ws domain.Workspace, v domain.Variable) {
	backend := w.findBackend(ws.BackendID)
	if backend == nil {
		slog.Error("save variable: backend not found", "id", ws.BackendID)
		return
	}
	upserter, ok := backend.(variableUpserter)
	if !ok {
		slog.Warn("backend does not support variable upsert", "id", ws.BackendID)
		return
	}
	release := w.locks.Acquire(ws.ID)
	defer release()
	if err := upserter.UpsertVariable(context.Background(), ws.ID, v); err != nil {
		slog.Error("upsert variable", "ws", ws.ID, "key", v.Key, "err", err)
		w.ToastError("Couldn't save " + v.Key + ": " + err.Error())
		return
	}
	slog.Info("variable saved", "ws", ws.ID, "key", v.Key, "sensitive", v.Sensitive)
	w.Toast("Saved " + v.Key)
	w.workspacePage.RefreshVariables()
}

