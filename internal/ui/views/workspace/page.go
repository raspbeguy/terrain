// Package workspace owns the per-workspace detail view: tabs for Overview,
// Runs, Variables, and State. Each tab is mostly empty in M1 — content fills
// in at M2 (runs), M3 (variables, state).
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

// Page is the workspace detail widget. Bind() updates it from a Workspace
// model; the same Page instance is reused as the user clicks between
// workspaces.
type Page struct {
	root *gtk.Box

	pathRow            *adw.ActionRow
	engineRow          *adw.ActionRow
	versionRow         *adw.ActionRow
	resourcesRow       *adw.ActionRow
	serialRow          *adw.ActionRow
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

	// stateVersions holds the currently displayed list, indexed by combo
	// position (0 = live; 1+ = snapshot in display order). Cached so the
	// version-changed handler can look up by index.
	stateVersions []domain.StateVersion

	// runs holds the current displayed list — index matches ListBoxRow.Index().
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
}

// LineageWarning is returned by the load-state-versions callback when the
// most recent snapshot's lineage differs from the previous one — typically
// the result of a `tofu init -reconfigure` or state surgery.
type LineageWarning struct {
	From string
	To   string
}

// New loads the workspace detail layout from gresource.
func New() *Page {
	builder := gtk.NewBuilderFromResource(uiResource)
	p := &Page{
		root:            uihelpers.MustCast[*gtk.Box](builder, "workspace_detail_root"),
		pathRow:         uihelpers.MustCast[*adw.ActionRow](builder, "workspace_path_row"),
		engineRow:       uihelpers.MustCast[*adw.ActionRow](builder, "workspace_engine_row"),
		versionRow:      uihelpers.MustCast[*adw.ActionRow](builder, "workspace_version_row"),
		resourcesRow:    uihelpers.MustCast[*adw.ActionRow](builder, "workspace_resources_row"),
		serialRow:       uihelpers.MustCast[*adw.ActionRow](builder, "workspace_serial_row"),
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

// SetOnNewPlan wires the callback for the "New Plan" button. Window owns
// run lifecycle, so it sets this once after New().
func (p *Page) SetOnNewPlan(fn func(domain.Workspace)) { p.onNewPlan = fn }

// SetOnLoadState wires the callback the State tab uses to fetch the current
// state. The callback runs synchronously and may take seconds; we accept
// that for now since `tofu show -json` is fast on local backends.
func (p *Page) SetOnLoadState(fn func(domain.Workspace) (*tfjson.State, error)) {
	p.onLoadState = fn
}

// SetOnLoadStateVersions wires the callback that returns the available
// state-version snapshots for a workspace, plus an optional lineage
// warning when the most recent snapshot's lineage differs from its
// predecessor.
func (p *Page) SetOnLoadStateVersions(fn func(domain.Workspace) ([]domain.StateVersion, *LineageWarning, error)) {
	p.onLoadStateVersions = fn
}

// SetOnLoadStateVersion wires the callback that fetches one specific
// snapshot's state JSON. Called when the user picks a non-Live entry from
// the version combo.
func (p *Page) SetOnLoadStateVersion(fn func(domain.Workspace, string) (*tfjson.State, error)) {
	p.onLoadStateVersion = fn
}

// SetOnCompareStates wires the "Compare…" button. Window opens the
// compare-versions dialog with the given workspace + already-loaded
// version list.
func (p *Page) SetOnCompareStates(fn func(domain.Workspace, []domain.StateVersion)) {
	p.onCompareStates = fn
}

// SetOnLoadRuns wires the callback used to populate the Runs tab.
func (p *Page) SetOnLoadRuns(fn func(domain.Workspace) ([]domain.Run, error)) {
	p.onLoadRuns = fn
}

// SetOnLoadVariables wires the callback the Variables tab uses to read
// declared variables and current overrides.
func (p *Page) SetOnLoadVariables(fn func(domain.Workspace) ([]domain.Variable, error)) {
	p.onLoadVars = fn
}

// SetOnEditVariable wires the row-click callback for the Variables list.
// The window opens the edit dialog and routes the save through the backend.
func (p *Page) SetOnEditVariable(fn func(domain.Workspace, domain.Variable)) {
	p.onEditVar = fn
}

// SetOnAddVariable wires the "Add Variable" button.
func (p *Page) SetOnAddVariable(fn func(domain.Workspace)) {
	p.onAddVar = fn
}

// RefreshVariables exposes the internal refresh hook so callers (window)
// can re-pull the list after a save without touching unexported state.
func (p *Page) RefreshVariables() { p.refreshVariables() }

// SetOnOpenRun wires the callback fired when the user clicks a run row.
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

// refreshStateVersionList rebuilds the version combo's StringList from the
// backend's snapshot list. Always puts "Live" at index 0, snapshots at 1+.
// Updates the lineage banner based on the warning the loader returns.
func (p *Page) refreshStateVersionList() {
	if p.onLoadStateVersions == nil {
		// No loader → just expose Live as the only option, hide banner.
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
		// Live: re-fetch via the live loader so the user sees the freshest
		// state, not the cached version.
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

// intToString is a tiny formatter so we don't drag strconv just for a
// status row; same idea as the dialog's local itoa.
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

// bindRuns populates the Runs list. Newest first (history is appended in
// chronological order, so we reverse on display).
func (p *Page) bindRuns(runs []domain.Run) {
	// Reverse the chronological list so newest is at top.
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

	p.runsStack.SetVisibleChildName("list")
	for _, r := range reversed {
		row := buildRunRow(r)
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

// buildRunRow renders one history entry as an AdwActionRow with status
// glyph, run kind, age, and (if errored) the error message as subtitle.
func buildRunRow(r domain.Run) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(string(r.Kind) + " · " + r.ID[:min(12, len(r.ID))])
	row.SetSubtitle(runRowSubtitle(r))
	row.SetActivatable(true)

	badge := gtk.NewLabel(runStatusGlyph(r.Status))
	badge.AddCSSClass("pill")
	badge.AddCSSClass(runStatusCSSClass(r.Status))
	badge.SetVAlign(gtk.AlignCenter)
	row.AddSuffix(badge)
	return row
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

// Root returns the top-level widget for embedding into a parent container.
func (p *Page) Root() *gtk.Box { return p.root }

// Bind populates the view from a Workspace. Called every time the user
// navigates to a different workspace. Refreshes the Runs list eagerly so the
// user sees past runs when they open a workspace.
func (p *Page) Bind(ws domain.Workspace) {
	slog.Debug("workspace bind", "id", ws.ID, "project", ws.ProjectName)
	p.current = ws
	p.pathRow.SetSubtitle(displayOrDash(ws.WorkingDirectory))
	p.engineRow.SetSubtitle(displayOrDash(ws.ExecutionMode))
	p.versionRow.SetSubtitle(displayOrDash(ws.TerraformVersion))
	// Resources / serial fill in once we have state — M3.
	p.refreshRuns()
	p.refreshVariables()
}

func displayOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
