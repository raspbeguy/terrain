package window

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

type Window struct {
	app      *adw.Application
	backends []domain.Backend
	locks    *runner.WorkspaceLocks

	root          *adw.ApplicationWindow
	toastOverlay  *adw.ToastOverlay
	sidebarStack  *gtk.Stack
	sidebarList   *gtk.ListBox
	sidebarSearch *gtk.SearchEntry
	contentStack  *gtk.Stack
	contentTitle  *adw.WindowTitle
	workspaceBin  *adw.Bin
	workspacePage *workspace.Page
	runPage       *run.Page

	searchFilter string

	// Row order; index matches ListBoxRow.Index().
	workspaces []domain.Workspace
	current    domain.Workspace

	onRemoveProject     func(domain.Workspace)
	onSync              func(domain.Workspace)
	onOpenDirectory     func(domain.Workspace)
	onAddSubpath        func(domain.Workspace)
	onNewWorkspace      func(domain.Workspace)
	onDeleteWorkspace   func(domain.Workspace)
	onRefreshWorkspaces func(domain.Workspace)
	onCheckBinary       func(domain.Workspace) string
	onOpenBinaryPrefs   func()
}

func (w *Window) SetOnRemoveProject(fn func(domain.Workspace)) {
	w.onRemoveProject = fn
}

func (w *Window) SetOnSync(fn func(domain.Workspace)) {
	w.onSync = fn
}

func (w *Window) SetOnOpenDirectory(fn func(domain.Workspace)) {
	w.onOpenDirectory = fn
}

func (w *Window) SetOnAddSubpath(fn func(domain.Workspace)) {
	w.onAddSubpath = fn
}

func (w *Window) SetOnNewWorkspace(fn func(domain.Workspace)) {
	w.onNewWorkspace = fn
}

func (w *Window) SetOnDeleteWorkspace(fn func(domain.Workspace)) {
	w.onDeleteWorkspace = fn
}

func (w *Window) SetOnRefreshWorkspaces(fn func(domain.Workspace)) {
	w.onRefreshWorkspaces = fn
}

func (w *Window) SetOnCheckBinary(fn func(domain.Workspace) string) {
	w.onCheckBinary = fn
}

func (w *Window) SetOnOpenBinaryPrefs(fn func()) {
	w.onOpenBinaryPrefs = fn
}

func (w *Window) SetWorkspacePageSyncBusy(busy bool) {
	w.workspacePage.SetSyncBusy(busy)
}

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
		sidebarSearch: uihelpers.MustCast[*gtk.SearchEntry](builder, "sidebar_search_entry"),
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
	w.workspacePage.SetOnSync(func(ws domain.Workspace) {
		if w.onSync != nil {
			w.onSync(ws)
		}
	})
	w.workspacePage.SetOnOpenDirectory(func(ws domain.Workspace) {
		if w.onOpenDirectory != nil {
			w.onOpenDirectory(ws)
		}
	})
	w.workspacePage.SetOnCheckBinary(func(ws domain.Workspace) string {
		if w.onCheckBinary != nil {
			return w.onCheckBinary(ws)
		}
		return ""
	})
	w.workspacePage.SetOnOpenBinaryPrefs(func() {
		if w.onOpenBinaryPrefs != nil {
			w.onOpenBinaryPrefs()
		}
	})
	w.runPage.SetOnBack(w.showWorkspaceView)
	w.runPage.SetOnStatus(func(status domain.RunStatus, _ string) {
		w.contentTitle.SetSubtitle(string(status))
	})

	w.sidebarList.ConnectRowActivated(w.onRowActivated)
	w.sidebarList.SetHeaderFunc(w.sidebarHeaderFunc)
	w.sidebarList.SetFilterFunc(w.sidebarFilter)
	w.sidebarSearch.ConnectSearchChanged(func() {
		w.searchFilter = strings.ToLower(strings.TrimSpace(w.sidebarSearch.Text()))
		w.sidebarList.InvalidateFilter()
	})

	if err := w.refreshFrom(backends); err != nil {
		slog.Warn("sidebar populate", "err", err)
	}

	slog.Info("window built",
		"backends", len(backends),
		"workspaces", len(w.workspaces),
	)
	return w, nil
}

func (w *Window) Present() { w.root.Present() }

func (w *Window) GtkWindow() *gtk.Window { return &w.root.Window }

// Must be called on the GTK main thread.
func (w *Window) Toast(message string) {
	if w.toastOverlay == nil {
		return
	}
	t := adw.NewToast(message)
	t.SetTimeout(4)
	w.toastOverlay.AddToast(t)
}

func (w *Window) ToastError(message string) {
	if w.toastOverlay == nil {
		return
	}
	t := adw.NewToast(message)
	t.SetTimeout(8)
	t.SetPriority(adw.ToastPriorityHigh)
	w.toastOverlay.AddToast(t)
}

func (w *Window) Refresh(backends []domain.Backend) error {
	return w.refreshFrom(backends)
}

// Remote backends load asynchronously so a slow OTF list never blocks startup.
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

// Stream when available; orgs with hundreds of workspaces blew past the prior synchronous deadline.
func (w *Window) fetchRemoteWorkspaces(b domain.Backend) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	streamer, ok := b.(domain.WorkspaceStreamer)
	if !ok {
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
		return
	}

	bridge.OnMainThread(func() {
		w.replaceBackendWorkspaces(b.ID(), nil)
	})
	total := 0
	for item := range streamer.StreamWorkspaces(ctx) {
		if item.Err != nil {
			err := item.Err
			bridge.OnMainThread(func() {
				slog.Warn("fetch remote workspaces", "backend", b.ID(), "err", err)
				w.ToastError("Couldn't load workspaces from " + b.DisplayName() + ": " + err.Error())
			})
			return
		}
		page := item.Workspaces
		total += len(page)
		bridge.OnMainThread(func() {
			w.appendBackendWorkspaces(b.ID(), page)
		})
	}
	slog.Info("remote workspaces loaded", "backend", b.ID(), "count", total)
}

func (w *Window) appendBackendWorkspaces(backendID string, more []domain.Workspace) {
	if len(more) == 0 {
		return
	}
	w.workspaces = append(w.workspaces, more...)
	w.rebuildSidebar()
}

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
		if ws.BackendID != "" && isLocalBackendID(w.backends, ws.BackendID) {
			row.SetTitle(ws.Name)
			row.AddCSSClass("terrain-workspace-row")
		} else {
			row.SetTitle(ws.ProjectName)
			row.SetSubtitle(ws.Name)
		}
		row.SetActivatable(true)
		w.attachRowKebab(row, ws)
		w.sidebarList.Append(row)
	}

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

func (w *Window) attachRowKebab(row *adw.ActionRow, ws domain.Workspace) {
	if ws.BackendID == "" || !isLocalBackendID(w.backends, ws.BackendID) {
		return
	}
	if ws.Name == "default" {
		return
	}

	popover := gtk.NewPopover()
	popover.SetHasArrow(true)
	box := gtk.NewBox(gtk.OrientationVertical, 0)

	deleteBtn := gtk.NewButtonWithLabel("Delete workspace")
	deleteBtn.AddCSSClass("flat")
	deleteBtn.AddCSSClass("destructive-action")
	deleteBtn.SetHAlign(gtk.AlignStart)
	deleteBtn.ConnectClicked(func() {
		popover.Popdown()
		w.confirmDeleteWorkspace(ws)
	})
	box.Append(deleteBtn)
	popover.SetChild(box)

	menu := gtk.NewMenuButton()
	menu.SetIconName("view-more-symbolic")
	menu.AddCSSClass("flat")
	menu.SetVAlign(gtk.AlignCenter)
	menu.SetPopover(popover)
	menu.SetTooltipText("More actions")

	row.AddSuffix(menu)
}

func (w *Window) projectHeaderWidget(ws domain.Workspace) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationHorizontal, 8)
	box.SetMarginTop(8)
	box.SetMarginStart(12)
	box.SetMarginEnd(8)
	box.SetMarginBottom(2)

	titleBox := gtk.NewBox(gtk.OrientationVertical, 0)
	titleBox.SetHExpand(true)
	title := gtk.NewLabel(ws.ProjectName)
	title.AddCSSClass("heading")
	title.SetXAlign(0)
	titleBox.Append(title)
	if ws.GitURL != "" {
		ref := ws.GitRef
		if ref == "" {
			ref = "default branch"
		}
		sub := gtk.NewLabel(ws.GitURL + " @ " + ref)
		sub.AddCSSClass("caption")
		sub.AddCSSClass("dim-label")
		sub.SetXAlign(0)
		sub.SetEllipsize(3)
		titleBox.Append(sub)
	}
	box.Append(titleBox)

	popover := gtk.NewPopover()
	popover.SetHasArrow(true)
	pbox := gtk.NewBox(gtk.OrientationVertical, 0)

	newBtn := gtk.NewButtonWithLabel("New workspace…")
	newBtn.AddCSSClass("flat")
	newBtn.SetHAlign(gtk.AlignStart)
	newBtn.ConnectClicked(func() {
		popover.Popdown()
		if w.onNewWorkspace != nil {
			w.onNewWorkspace(ws)
		}
	})
	pbox.Append(newBtn)

	refreshBtn := gtk.NewButtonWithLabel("Refresh workspaces")
	refreshBtn.AddCSSClass("flat")
	refreshBtn.SetHAlign(gtk.AlignStart)
	refreshBtn.ConnectClicked(func() {
		popover.Popdown()
		if w.onRefreshWorkspaces != nil {
			w.onRefreshWorkspaces(ws)
		}
	})
	pbox.Append(refreshBtn)

	if ws.GitURL != "" {
		addSubpathBtn := gtk.NewButtonWithLabel("Add another subpath from this repo…")
		addSubpathBtn.AddCSSClass("flat")
		addSubpathBtn.SetHAlign(gtk.AlignStart)
		addSubpathBtn.ConnectClicked(func() {
			popover.Popdown()
			if w.onAddSubpath != nil {
				w.onAddSubpath(ws)
			}
		})
		pbox.Append(addSubpathBtn)
	}

	removeBtn := gtk.NewButtonWithLabel("Remove project")
	removeBtn.AddCSSClass("flat")
	removeBtn.AddCSSClass("destructive-action")
	removeBtn.SetHAlign(gtk.AlignStart)
	removeBtn.ConnectClicked(func() {
		popover.Popdown()
		w.confirmRemoveProject(ws)
	})
	pbox.Append(removeBtn)

	popover.SetChild(pbox)

	menu := gtk.NewMenuButton()
	menu.SetIconName("view-more-symbolic")
	menu.AddCSSClass("flat")
	menu.SetVAlign(gtk.AlignCenter)
	menu.SetPopover(popover)
	menu.SetTooltipText("Project actions")
	box.Append(menu)

	return box
}

func isLocalBackendID(backends []domain.Backend, id string) bool {
	for _, b := range backends {
		if b.ID() == id {
			return b.Kind() == domain.BackendKindLocal
		}
	}
	return false
}

func (w *Window) confirmDeleteWorkspace(ws domain.Workspace) {
	dlg := adw.NewAlertDialog(
		"Delete workspace?",
		fmt.Sprintf("This deletes workspace %q from %q. The on-disk Terraform state for this workspace is wiped; this cannot be undone.", ws.Name, ws.ProjectName),
	)
	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("delete", "Delete")
	dlg.SetResponseAppearance("delete", adw.ResponseDestructive)
	dlg.SetDefaultResponse("cancel")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp == "delete" && w.onDeleteWorkspace != nil {
			w.onDeleteWorkspace(ws)
		}
	})
	dlg.Present(&w.root.Window)
}

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

func (w *Window) sidebarFilter(row *gtk.ListBoxRow) bool {
	if w.searchFilter == "" {
		return true
	}
	idx := row.Index()
	if idx < 0 || int(idx) >= len(w.workspaces) {
		return false
	}
	ws := w.workspaces[idx]
	hay := strings.ToLower(ws.ProjectName + " " + ws.Name)
	if backend := w.findBackend(ws.BackendID); backend != nil {
		hay += " " + strings.ToLower(backend.DisplayName())
	}
	return strings.Contains(hay, w.searchFilter)
}

func (w *Window) sidebarHeaderFunc(row, before *gtk.ListBoxRow) {
	idx := row.Index()
	if idx < 0 || int(idx) >= len(w.workspaces) {
		row.SetHeader(nil)
		return
	}
	cur := w.workspaces[idx]
	var prev *domain.Workspace
	if before != nil {
		prevIdx := before.Index()
		if prevIdx >= 0 && int(prevIdx) < len(w.workspaces) {
			p := w.workspaces[prevIdx]
			prev = &p
		}
	}

	switch {
	case prev == nil || prev.BackendID != cur.BackendID:
		row.SetHeader(w.backendOrProjectHeader(cur, true))
	case isLocalBackendID(w.backends, cur.BackendID) && prev.ProjectID != cur.ProjectID:
		row.SetHeader(w.projectHeaderWidget(cur))
	default:
		row.SetHeader(nil)
	}
}

func (w *Window) backendOrProjectHeader(ws domain.Workspace, includeBackendLabel bool) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	if includeBackendLabel {
		backend := w.findBackend(ws.BackendID)
		name := ws.BackendID
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
		box.Append(label)
	}
	if isLocalBackendID(w.backends, ws.BackendID) {
		box.Append(w.projectHeaderWidget(ws))
	}
	return box
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
		nil,
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

type stateLoader interface {
	LoadState(ctx context.Context, workspaceID string) (*tfjson.State, error)
}

// TryAcquire so a refresh during a run reports cleanly instead of hitting tofu's state-lock.
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

type stateVersionLister interface {
	StateVersions(ctx context.Context, workspaceID string) ([]domain.StateVersion, error)
	LoadStateVersion(ctx context.Context, workspaceID, versionID string) (*tfjson.State, error)
}

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

// StateVersions returns newest-first.
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

func (w *Window) compareStates(ws domain.Workspace, versions []domain.StateVersion) {
	loader := func(versionID string) (*tfjson.State, error) {
		if versionID == "" {
			return w.loadState(ws)
		}
		return w.loadStateVersion(ws, versionID)
	}
	dialogs.PresentStateDiff(w.GtkWindow(), versions, loader)
}

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
		return nil, nil
	}
	return listing.Runs(context.Background(), ws.ID)
}

func (w *Window) openRun(ws domain.Workspace, r domain.Run) {
	slog.Info("open historical run", "id", r.ID, "kind", r.Kind, "status", r.Status, "ws", ws.ID)
	w.workspaceBin.SetChild(w.runPage.Root())
	w.contentTitle.SetTitle(ws.ProjectName + " · " + ws.Name)
	w.runPage.LoadHistory(r)
}

type variableLoader interface {
	VariablesForWorkspace(ctx context.Context, workspaceID string) ([]domain.Variable, error)
}

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

type variableUpserter interface {
	UpsertVariable(ctx context.Context, workspaceID string, v domain.Variable) error
}

// Local-only; remote backends manage execution mode server-side.
func (w *Window) openWorkspaceSettings(ws domain.Workspace) {
	if !isLocalBackendID(w.backends, ws.BackendID) {
		w.Toast("Workspace settings are local-backend only")
		return
	}
	dlg := dialogs.NewWorkspaceSettings(ws.BackendID, ws.ID)
	dlg.Present(w.GtkWindow())
}

func (w *Window) addVariable(ws domain.Workspace) {
	dialogs.EditVariable(w.GtkWindow(), dialogs.VarEditAdd, domain.Variable{},
		func(v domain.Variable) { w.saveVariable(ws, v) })
}

func (w *Window) editVariable(ws domain.Workspace, v domain.Variable) {
	dialogs.EditVariable(w.GtkWindow(), dialogs.VarEditEdit, v,
		func(saved domain.Variable) { w.saveVariable(ws, saved) })
}

type variableDeleter interface {
	DeleteVariable(ctx context.Context, workspaceID, key string) error
}

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

// Lock so an in-flight run can't materialise vars mid hclwrite read-modify-write.
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

