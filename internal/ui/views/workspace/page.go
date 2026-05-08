// Package workspace owns the per-workspace detail view (Overview / Runs
// / Variables / State tabs).
package workspace

import (
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
	"github.com/raspbeguy/terrain/internal/ui/widgets"
)

const uiResource = "/io/github/raspbeguy/Terrain/workspace-detail.ui"

// Page is reused across workspaces; Bind() updates it.
type Page struct {
	root *gtk.Box

	repoRow            *adw.ActionRow
	subpathRow         *adw.ActionRow
	openDirBtn         *gtk.Button
	syncBtn            *gtk.Button
	binaryBanner       *adw.Banner
	engineRow          *adw.ActionRow
	versionRow         *adw.ActionRow
	resourcesRow       *adw.ActionRow
	serialRow          *adw.ActionRow
	settingsBtn        *gtk.Button
	newPlanBtn         *gtk.Button
	refreshStateBtn    *gtk.Button
	compareStateBtn    *gtk.Button
	refreshRunsBtn     *gtk.Button
	refreshVarsBtn     *gtk.Button
	addVarBtn          *gtk.Button
	runsStack          *gtk.Stack
	runsListBox        *gtk.ListBox
	stateBanner        *adw.Banner
	stateVersionCombo  *adw.ComboRow

	stateTree *widgets.StateTree
	varList   *widgets.VarList

	// stateVersions index matches the combo position (0 = live; 1+ =
	// snapshots in display order).
	stateVersions []domain.StateVersion
	// runs index matches ListBoxRow.Index().
	runs []domain.Run

	current             domain.Workspace
	onNewPlan           func(domain.Workspace)
	onLoadState         func(domain.Workspace) (*tfjson.State, error)
	onLoadRuns          func(domain.Workspace) ([]domain.Run, error)
	onLoadVars          func(domain.Workspace) ([]domain.Variable, error)
	onLoadStateVersions func(domain.Workspace) ([]domain.StateVersion, *LineageWarning, error)
	onLoadStateVersion  func(domain.Workspace, string) (*tfjson.State, error)
	onCompareStates     func(domain.Workspace, []domain.StateVersion)
	onOpenRun           func(domain.Workspace, domain.Run)
	onEditVar           func(domain.Workspace, domain.Variable)
	onAddVar            func(domain.Workspace)
	onRemoveVar         func(domain.Workspace, domain.Variable)
	onOpenSettings      func(domain.Workspace)
	onSync              func(domain.Workspace)
	onOpenDirectory     func(domain.Workspace)
	onCheckBinary       func(domain.Workspace) string
	onOpenBinaryPrefs   func()
}

func (p *Page) SetOnOpenSettings(fn func(domain.Workspace)) {
	p.onOpenSettings = fn
}

// LineageWarning flags a state-tree replacement (state rm + import,
// `tofu init -reconfigure`).
type LineageWarning struct {
	From string
	To   string
}

func New() *Page {
	builder := gtk.NewBuilderFromResource(uiResource)
	p := &Page{
		root:            uihelpers.MustCast[*gtk.Box](builder, "workspace_detail_root"),
		repoRow:         uihelpers.MustCast[*adw.ActionRow](builder, "workspace_repo_row"),
		subpathRow:      uihelpers.MustCast[*adw.ActionRow](builder, "workspace_subpath_row"),
		openDirBtn:      uihelpers.MustCast[*gtk.Button](builder, "workspace_open_dir_button"),
		syncBtn:         uihelpers.MustCast[*gtk.Button](builder, "workspace_sync_button"),
		binaryBanner:    uihelpers.MustCast[*adw.Banner](builder, "workspace_binary_banner"),
		engineRow:       uihelpers.MustCast[*adw.ActionRow](builder, "workspace_engine_row"),
		versionRow:      uihelpers.MustCast[*adw.ActionRow](builder, "workspace_version_row"),
		resourcesRow:    uihelpers.MustCast[*adw.ActionRow](builder, "workspace_resources_row"),
		serialRow:       uihelpers.MustCast[*adw.ActionRow](builder, "workspace_serial_row"),
		settingsBtn:     uihelpers.MustCast[*gtk.Button](builder, "workspace_settings_button"),
		newPlanBtn:      uihelpers.MustCast[*gtk.Button](builder, "workspace_new_plan_button"),
		refreshStateBtn:    uihelpers.MustCast[*gtk.Button](builder, "workspace_refresh_state_button"),
		compareStateBtn:    uihelpers.MustCast[*gtk.Button](builder, "workspace_compare_state_button"),
		stateBanner:        uihelpers.MustCast[*adw.Banner](builder, "workspace_state_banner"),
		stateVersionCombo:  uihelpers.MustCast[*adw.ComboRow](builder, "workspace_state_version_row"),
		refreshRunsBtn:  uihelpers.MustCast[*gtk.Button](builder, "workspace_refresh_runs_button"),
		refreshVarsBtn:  uihelpers.MustCast[*gtk.Button](builder, "workspace_refresh_vars_button"),
		addVarBtn:       uihelpers.MustCast[*gtk.Button](builder, "workspace_add_var_button"),
		runsStack:       uihelpers.MustCast[*gtk.Stack](builder, "workspace_runs_stack"),
		runsListBox:     uihelpers.MustCast[*gtk.ListBox](builder, "workspace_runs_listbox"),
		stateTree:       widgets.NewStateTree(),
		varList:         widgets.NewVarList(),
	}

	stateBin := uihelpers.MustCast[*adw.Bin](builder, "workspace_state_container")
	stateBin.SetChild(p.stateTree.Root())

	varsBin := uihelpers.MustCast[*adw.Bin](builder, "workspace_variables_container")
	varsBin.SetChild(p.varList.Root())

	p.newPlanBtn.ConnectClicked(func() {
		slog.Debug("new plan button clicked", "ws", p.current.ID)
		if p.onNewPlan != nil && p.current.ID != "" {
			p.onNewPlan(p.current)
		}
	})
	p.settingsBtn.ConnectClicked(func() {
		slog.Debug("workspace settings clicked", "ws", p.current.ID)
		if p.onOpenSettings != nil && p.current.ID != "" {
			p.onOpenSettings(p.current)
		}
	})
	p.openDirBtn.ConnectClicked(func() {
		if p.onOpenDirectory != nil && p.current.ID != "" {
			p.onOpenDirectory(p.current)
		}
	})
	p.syncBtn.ConnectClicked(func() {
		if p.onSync != nil && p.current.ID != "" {
			p.onSync(p.current)
		}
	})
	p.refreshStateBtn.ConnectClicked(func() {
		slog.Debug("refresh state clicked", "ws", p.current.ID)
		p.refreshState()
	})
	p.compareStateBtn.ConnectClicked(func() {
		slog.Debug("compare state clicked", "ws", p.current.ID)
		if p.onCompareStates != nil && p.current.ID != "" {
			p.onCompareStates(p.current, p.stateVersions)
		}
	})
	p.stateVersionCombo.Connect("notify::selected", func() {
		p.onStateVersionChanged()
	})
	p.refreshRunsBtn.ConnectClicked(func() {
		slog.Debug("refresh runs clicked", "ws", p.current.ID)
		p.refreshRuns()
	})
	p.refreshVarsBtn.ConnectClicked(func() {
		slog.Debug("refresh vars clicked", "ws", p.current.ID)
		p.refreshVariables()
	})
	p.addVarBtn.ConnectClicked(func() {
		slog.Debug("add var clicked", "ws", p.current.ID)
		if p.onAddVar != nil && p.current.ID != "" {
			p.onAddVar(p.current)
		}
	})
	p.varList.SetOnActivate(func(v domain.Variable) {
		slog.Debug("edit var clicked", "ws", p.current.ID, "key", v.Key)
		if p.onEditVar != nil && p.current.ID != "" {
			p.onEditVar(p.current, v)
		}
	})
	p.runsListBox.ConnectRowActivated(p.onRunRowActivated)
	return p
}

func (p *Page) SetOnNewPlan(fn func(domain.Workspace)) { p.onNewPlan = fn }

func (p *Page) SetOnLoadState(fn func(domain.Workspace) (*tfjson.State, error)) {
	p.onLoadState = fn
}

func (p *Page) SetOnLoadStateVersions(fn func(domain.Workspace) ([]domain.StateVersion, *LineageWarning, error)) {
	p.onLoadStateVersions = fn
}

func (p *Page) SetOnLoadStateVersion(fn func(domain.Workspace, string) (*tfjson.State, error)) {
	p.onLoadStateVersion = fn
}

func (p *Page) SetOnCompareStates(fn func(domain.Workspace, []domain.StateVersion)) {
	p.onCompareStates = fn
}

func (p *Page) SetOnLoadRuns(fn func(domain.Workspace) ([]domain.Run, error)) {
	p.onLoadRuns = fn
}

func (p *Page) SetOnLoadVariables(fn func(domain.Workspace) ([]domain.Variable, error)) {
	p.onLoadVars = fn
}

func (p *Page) SetOnEditVariable(fn func(domain.Workspace, domain.Variable)) {
	p.onEditVar = fn
}

func (p *Page) SetOnAddVariable(fn func(domain.Workspace)) {
	p.onAddVar = fn
}

func (p *Page) SetOnRemoveVariable(fn func(domain.Workspace, domain.Variable)) {
	p.onRemoveVar = fn
	if fn == nil {
		p.varList.SetOnRemove(nil)
		return
	}
	p.varList.SetOnRemove(func(v domain.Variable) {
		if p.current.ID != "" {
			fn(p.current, v)
		}
	})
}

func (p *Page) RefreshVariables() { p.refreshVariables() }

func (p *Page) SetOnOpenRun(fn func(domain.Workspace, domain.Run)) {
	p.onOpenRun = fn
}

func (p *Page) refreshState() {
	if p.current.ID == "" {
		return
	}
	// Re-fetch the snapshot list first so the combo reflects what's on
	// disk; rebinding to "Live" happens implicitly when we set selected=0.
	p.refreshStateVersionList()

	if p.onLoadState == nil {
		return
	}
	state, err := p.onLoadState(p.current)
	if err != nil {
		slog.Warn("load state", "ws", p.current.ID, "err", err)
		p.stateTree.SetError(err.Error())
		return
	}
	p.stateTree.Bind(state)
}

func (p *Page) refreshStateVersionList() {
	if p.onLoadStateVersions == nil {
		p.stateVersions = nil
		p.populateStateVersionCombo(nil)
		p.stateBanner.SetRevealed(false)
		return
	}
	versions, warn, err := p.onLoadStateVersions(p.current)
	if err != nil {
		slog.Warn("load state versions", "ws", p.current.ID, "err", err)
		versions = nil
	}
	p.stateVersions = versions
	p.populateStateVersionCombo(versions)

	if warn != nil {
		p.stateBanner.SetTitle("Lineage changed: " + warn.From + " → " + warn.To +
			". The state may have been re-initialized or imported.")
		p.stateBanner.SetRevealed(true)
	} else {
		p.stateBanner.SetRevealed(false)
	}
}

func (p *Page) populateStateVersionCombo(versions []domain.StateVersion) {
	model := gtk.NewStringList(nil)
	model.Append("Live (current)")
	for _, v := range versions {
		label := v.CreatedAt.Format("2006-01-02 15:04") + "  ·  serial " +
			intToString(v.Serial)
		if v.RunID != "" {
			label += "  ·  run " + truncateID(v.RunID, 8)
		}
		model.Append(label)
	}
	p.stateVersionCombo.SetModel(model)
	p.stateVersionCombo.SetSelected(0)
}

func (p *Page) onStateVersionChanged() {
	idx := int(p.stateVersionCombo.Selected())
	if idx <= 0 {
		// Live: re-fetch so the freshest state shows, not a cached one.
		if p.onLoadState != nil && p.current.ID != "" {
			state, err := p.onLoadState(p.current)
			if err != nil {
				p.stateTree.SetError(err.Error())
				return
			}
			p.stateTree.Bind(state)
		}
		return
	}
	versionIdx := idx - 1
	if versionIdx >= len(p.stateVersions) {
		return
	}
	v := p.stateVersions[versionIdx]
	if p.onLoadStateVersion == nil {
		return
	}
	state, err := p.onLoadStateVersion(p.current, v.ID)
	if err != nil {
		slog.Warn("load state version", "ws", p.current.ID, "id", v.ID, "err", err)
		p.stateTree.SetError(err.Error())
		return
	}
	p.stateTree.Bind(state)
}

// intToString avoids dragging in strconv just for a status-row formatter.
func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var b [21]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func truncateID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (p *Page) refreshRuns() {
	if p.onLoadRuns == nil || p.current.ID == "" {
		return
	}
	runs, err := p.onLoadRuns(p.current)
	if err != nil {
		slog.Warn("load runs", "ws", p.current.ID, "err", err)
		return
	}
	p.bindRuns(runs)
}

func (p *Page) refreshVariables() {
	if p.onLoadVars == nil || p.current.ID == "" {
		return
	}
	vars, err := p.onLoadVars(p.current)
	if err != nil {
		slog.Warn("load variables", "ws", p.current.ID, "err", err)
		p.varList.SetError(err.Error())
		return
	}
	p.varList.Bind(vars)
}

// bindRuns reverses the chronological list (newest at top) and tags
// plan rows whose plan file was consumed by an apply.
func (p *Page) bindRuns(runs []domain.Run) {
	reversed := make([]domain.Run, len(runs))
	for i, r := range runs {
		reversed[len(runs)-1-i] = r
	}
	p.runs = reversed

	for child := p.runsListBox.RowAtIndex(0); child != nil; child = p.runsListBox.RowAtIndex(0) {
		p.runsListBox.Remove(child)
	}

	if len(reversed) == 0 {
		p.runsStack.SetVisibleChildName("empty")
		return
	}

	// runs is oldest-first, so the latest apply attempt wins.
	appliedPlans := map[string]domain.RunStatus{}
	for _, r := range runs {
		if r.Kind == domain.RunKindApply && r.PlanFile != "" {
			appliedPlans[r.PlanFile] = r.Status
		}
	}

	p.runsStack.SetVisibleChildName("list")
	for _, r := range reversed {
		row := buildRunRow(r, appliedPlans)
		p.runsListBox.Append(row)
	}
}

func (p *Page) onRunRowActivated(row *gtk.ListBoxRow) {
	idx := row.Index()
	if idx < 0 || int(idx) >= len(p.runs) {
		return
	}
	if p.onOpenRun != nil {
		p.onOpenRun(p.current, p.runs[idx])
	}
}

// buildRunRow attaches an outcome badge (→ applied / errored / canceled)
// when r's plan file was consumed by a later apply.
func buildRunRow(r domain.Run, appliedPlans map[string]domain.RunStatus) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(string(r.Kind) + " · " + r.ID[:min(12, len(r.ID))])
	row.SetSubtitle(runRowSubtitle(r))
	row.SetActivatable(true)

	if (r.Kind == domain.RunKindPlan || r.Kind == domain.RunKindDestroy) && r.PlanFile != "" {
		if applyStatus, ok := appliedPlans[r.PlanFile]; ok {
			pill := gtk.NewLabel(planOutcomeBadge(applyStatus))
			pill.AddCSSClass("pill")
			pill.AddCSSClass(runStatusCSSClass(applyStatus))
			pill.SetVAlign(gtk.AlignCenter)
			pill.SetTooltipText("This plan was used by a later apply run")
			row.AddSuffix(pill)
		}
	}

	badge := gtk.NewLabel(runStatusGlyph(r.Status))
	badge.AddCSSClass("pill")
	badge.AddCSSClass(runStatusCSSClass(r.Status))
	badge.SetVAlign(gtk.AlignCenter)
	row.AddSuffix(badge)
	return row
}

func planOutcomeBadge(applyStatus domain.RunStatus) string {
	switch applyStatus {
	case domain.StatusApplied:
		return "→ applied"
	case domain.StatusErrored:
		return "→ apply errored"
	case domain.StatusCanceled:
		return "→ apply canceled"
	}
	return "→ " + string(applyStatus)
}

func runRowSubtitle(r domain.Run) string {
	when := r.CreatedAt.Format("2006-01-02 15:04")
	switch r.Status {
	case domain.StatusErrored:
		return when + " · errored"
	case domain.StatusCanceled:
		return when + " · canceled"
	case domain.StatusApplied:
		return when + " · applied"
	case domain.StatusPlanned:
		return when + " · planned"
	}
	return when + " · " + string(r.Status)
}

func runStatusGlyph(s domain.RunStatus) string {
	switch s {
	case domain.StatusApplied:
		return "✓"
	case domain.StatusPlanned:
		return "✓"
	case domain.StatusErrored:
		return "!"
	case domain.StatusCanceled:
		return "·"
	case domain.StatusDiscarded:
		return "·"
	}
	return "…"
}

func runStatusCSSClass(s domain.RunStatus) string {
	switch s {
	case domain.StatusApplied:
		return "success"
	case domain.StatusPlanned:
		return "accent"
	case domain.StatusErrored:
		return "error"
	case domain.StatusCanceled, domain.StatusDiscarded:
		return "dim-label"
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (p *Page) Root() *gtk.Box { return p.root }

func (p *Page) Bind(ws domain.Workspace) {
	slog.Debug("workspace bind", "id", ws.ID, "project", ws.ProjectName)
	p.current = ws

	if ws.GitURL != "" {
		ref := ws.GitRef
		if ref == "" {
			ref = "default branch"
		}
		p.repoRow.SetSubtitle(ws.GitURL + " @ " + ref)
		p.openDirBtn.SetVisible(true)
		p.syncBtn.SetVisible(true)
		if ws.Subpath != "" {
			p.subpathRow.SetSubtitle(ws.Subpath)
			p.subpathRow.SetVisible(true)
		} else {
			p.subpathRow.SetVisible(false)
		}
	} else {
		p.repoRow.SetSubtitle(displayOrDash(ws.WorkingDirectory))
		p.openDirBtn.SetVisible(false)
		p.syncBtn.SetVisible(false)
		p.subpathRow.SetVisible(false)
	}

	p.engineRow.SetSubtitle(displayOrDash(ws.ExecutionMode))
	p.versionRow.SetSubtitle(displayOrDash(ws.TerraformVersion))
	p.refreshBinaryBanner(ws)
	p.refreshRuns()
	p.refreshVariables()
}

func (p *Page) refreshBinaryBanner(ws domain.Workspace) {
	if p.onCheckBinary == nil {
		p.binaryBanner.SetRevealed(false)
		return
	}
	msg := p.onCheckBinary(ws)
	if msg == "" {
		p.binaryBanner.SetRevealed(false)
		return
	}
	p.binaryBanner.SetTitle(msg)
	p.binaryBanner.SetRevealed(true)
}

// SetOnSync wires the "Sync from git remote" suffix button.
func (p *Page) SetOnSync(fn func(domain.Workspace)) { p.onSync = fn }

// SetOnOpenDirectory wires the "Open workspace directory" suffix button.
func (p *Page) SetOnOpenDirectory(fn func(domain.Workspace)) { p.onOpenDirectory = fn }

// SetSyncBusy disables the sync button and shows a spinner-ish state.
func (p *Page) SetSyncBusy(busy bool) {
	p.syncBtn.SetSensitive(!busy)
}

// SetOnCheckBinary wires the binary-status check; empty return = no banner.
func (p *Page) SetOnCheckBinary(fn func(domain.Workspace) string) {
	p.onCheckBinary = fn
}

// SetOnOpenBinaryPrefs wires the banner's Open Preferences button.
func (p *Page) SetOnOpenBinaryPrefs(fn func()) {
	p.onOpenBinaryPrefs = fn
	p.binaryBanner.ConnectButtonClicked(func() {
		if p.onOpenBinaryPrefs != nil {
			p.onOpenBinaryPrefs()
		}
	})
}

func displayOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
