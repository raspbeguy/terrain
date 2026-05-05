package dialogs

import (
	"context"
	"log/slog"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const varsetsResource = "/io/github/raspbeguy/Terrain/varsets.ui"

type VarsetsBackend interface {
	VariableSets(ctx context.Context) ([]domain.VariableSet, error)
	VariableSet(ctx context.Context, setID string) (domain.VariableSet, error)
	CreateVariableSet(ctx context.Context, name, description string) (domain.VariableSet, error)
	DeleteVariableSet(ctx context.Context, setID string) error
	UpdateVariableSetMeta(ctx context.Context, setID string, meta domain.VariableSet) error
	UpsertVariableSetVar(ctx context.Context, setID string, v domain.Variable) error
	DeleteVariableSetVar(ctx context.Context, setID, key string) error
}

// ProjectChoice re-exports domain.ProjectChoice so callers don't need
// to import domain just for the type.
type ProjectChoice = domain.ProjectChoice

// VarsetsDialog hosts the variable-set management page (one instance
// per opening).
type VarsetsDialog struct {
	dialog *adw.Dialog
	nav    *adw.NavigationView

	listGroup *adw.PreferencesGroup
	newBtn    *gtk.Button

	detailNameRow    *adw.EntryRow
	detailDescRow    *adw.EntryRow
	detailScopeRow   *adw.ComboRow
	detailProjectRow *adw.ComboRow
	detailWsGrp      *adw.PreferencesGroup
	detailVarsGrp    *adw.PreferencesGroup
	detailAddVar     *gtk.Button
	detailDeleteBt   *gtk.Button

	projects   []ProjectChoice
	workspaces []domain.Workspace
	// wsRows is rebuilt on each openDetail rather than diffed.
	wsRows []*adw.SwitchRow
	// suppressNotify gates the autosave handlers while we populate the
	// form programmatically.
	suppressNotify bool

	backend VarsetsBackend
	current domain.VariableSet
}

func PresentVarsets(parent *gtk.Window, backend VarsetsBackend, projects []ProjectChoice, workspaces []domain.Workspace) {
	if backend == nil {
		slog.Warn("variable sets: backend not available (no local backend in registry)")
		return
	}
	d := newVarsetsDialog(backend, projects, workspaces)
	d.refreshList()
	d.dialog.Present(parent)
}

func newVarsetsDialog(backend VarsetsBackend, projects []ProjectChoice, workspaces []domain.Workspace) *VarsetsDialog {
	builder := gtk.NewBuilderFromResource(varsetsResource)
	d := &VarsetsDialog{
		dialog:           uihelpers.MustCast[*adw.Dialog](builder, "varsets_dialog"),
		nav:              uihelpers.MustCast[*adw.NavigationView](builder, "varsets_nav"),
		listGroup:        uihelpers.MustCast[*adw.PreferencesGroup](builder, "varsets_list_group"),
		newBtn:           uihelpers.MustCast[*gtk.Button](builder, "varsets_new_button"),
		detailNameRow:    uihelpers.MustCast[*adw.EntryRow](builder, "varsets_detail_name_row"),
		detailDescRow:    uihelpers.MustCast[*adw.EntryRow](builder, "varsets_detail_description_row"),
		detailScopeRow:   uihelpers.MustCast[*adw.ComboRow](builder, "varsets_detail_scope_row"),
		detailProjectRow: uihelpers.MustCast[*adw.ComboRow](builder, "varsets_detail_project_row"),
		detailWsGrp:      uihelpers.MustCast[*adw.PreferencesGroup](builder, "varsets_detail_workspaces_group"),
		detailVarsGrp:    uihelpers.MustCast[*adw.PreferencesGroup](builder, "varsets_detail_vars_group"),
		detailAddVar:     uihelpers.MustCast[*gtk.Button](builder, "varsets_detail_add_var_button"),
		detailDeleteBt:   uihelpers.MustCast[*gtk.Button](builder, "varsets_detail_delete_button"),
		backend:          backend,
		workspaces:       workspaces,
	}

	// Project picker is populated once; mid-dialog registry changes
	// aren't observed.
	d.projects = projects
	projectStrings := gtk.NewStringList(nil)
	for _, p := range d.projects {
		projectStrings.Append(p.Name)
	}
	d.detailProjectRow.SetModel(projectStrings)

	d.newBtn.ConnectClicked(d.onCreate)
	d.detailAddVar.ConnectClicked(d.onAddVar)
	d.detailDeleteBt.ConnectClicked(d.onDelete)

	// Autosave on change; suppressNotify guards initial population.
	d.detailNameRow.ConnectChanged(d.saveMeta)
	d.detailDescRow.ConnectChanged(d.saveMeta)
	d.detailScopeRow.Connect("notify::selected", func() {
		d.updateScopeVisibility()
		d.saveMeta()
	})
	d.detailProjectRow.Connect("notify::selected", d.saveMeta)

	return d
}

func (d *VarsetsDialog) refreshList() {
	clearGroup(d.listGroup)
	sets, err := d.backend.VariableSets(context.Background())
	if err != nil {
		slog.Warn("list variable sets", "err", err)
	}
	if len(sets) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No variable sets yet")
		empty.SetSubtitle("Click \"New Set\" above to create one.")
		empty.AddCSSClass("dim-label")
		d.listGroup.Add(empty)
		return
	}
	for _, set := range sets {
		row := buildVarsetRow(set)
		set := set
		row.ConnectActivated(func() { d.openDetail(set) })
		d.listGroup.Add(row)
	}
}

func buildVarsetRow(set domain.VariableSet) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(escapeMarkup(set.Name))
	subtitle := set.Description
	if subtitle == "" {
		subtitle = pluralize(len(set.Variables), "variable")
	} else {
		subtitle = pluralize(len(set.Variables), "variable") + " · " + subtitle
	}
	row.SetSubtitle(subtitle)
	row.SetActivatable(true)

	chevron := gtk.NewImageFromIconName("go-next-symbolic")
	chevron.AddCSSClass("dim-label")
	row.AddSuffix(chevron)
	return row
}

func (d *VarsetsDialog) openDetail(set domain.VariableSet) {
	d.current = set
	d.suppressNotify = true
	d.detailNameRow.SetText(set.Name)
	d.detailDescRow.SetText(set.Description)
	d.detailScopeRow.SetSelected(scopeToIndex(set.Scope))
	d.detailProjectRow.SetSelected(d.projectIndex(set.ProjectID))
	d.populateWorkspaceSwitches(set)
	d.updateScopeVisibility()
	d.suppressNotify = false
	d.populateDetailVars(set)
	d.nav.PushByTag("detail")
}

func (d *VarsetsDialog) updateScopeVisibility() {
	idx := d.detailScopeRow.Selected()
	d.detailProjectRow.SetVisible(idx == 1)
	d.detailWsGrp.SetVisible(idx == 2)
}

func (d *VarsetsDialog) populateWorkspaceSwitches(set domain.VariableSet) {
	for _, r := range d.wsRows {
		d.detailWsGrp.Remove(r)
	}
	d.wsRows = nil

	attached := map[string]bool{}
	for _, id := range set.Workspaces {
		attached[id] = true
	}

	if len(d.workspaces) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No workspaces")
		empty.SetSubtitle("Add a local project to populate this list.")
		empty.AddCSSClass("dim-label")
		d.detailWsGrp.Add(empty)
		return
	}

	for _, ws := range d.workspaces {
		ws := ws
		row := adw.NewSwitchRow()
		row.SetTitle(ws.ProjectName)
		if ws.Name != "" {
			row.SetSubtitle(ws.Name)
		}
		row.SetActive(attached[ws.ID])
		row.Connect("notify::active", func() {
			d.toggleWorkspace(ws.ID, row.Active())
		})
		d.detailWsGrp.Add(row)
		d.wsRows = append(d.wsRows, row)
	}
}

func (d *VarsetsDialog) toggleWorkspace(workspaceID string, attached bool) {
	if d.suppressNotify || d.current.ID == "" {
		return
	}
	out := make([]string, 0, len(d.current.Workspaces)+1)
	seen := false
	for _, id := range d.current.Workspaces {
		if id == workspaceID {
			seen = true
			if attached {
				out = append(out, id)
			}
			continue
		}
		out = append(out, id)
	}
	if attached && !seen {
		out = append(out, workspaceID)
	}
	d.current.Workspaces = out
	d.saveMeta()
}

func (d *VarsetsDialog) saveMeta() {
	if d.suppressNotify || d.current.ID == "" {
		return
	}
	meta := domain.VariableSet{
		Name:        d.detailNameRow.Text(),
		Description: d.detailDescRow.Text(),
		Scope:       indexToScope(d.detailScopeRow.Selected()),
		Workspaces:  d.current.Workspaces,
		ProjectID:   d.selectedProjectID(),
		Priority:    d.current.Priority,
	}
	if err := d.backend.UpdateVariableSetMeta(context.Background(), d.current.ID, meta); err != nil {
		slog.Warn("update varset meta", "id", d.current.ID, "err", err)
		return
	}
	d.current.Name = meta.Name
	d.current.Description = meta.Description
	d.current.Scope = meta.Scope
	d.current.ProjectID = meta.ProjectID
	d.refreshList()
}

func (d *VarsetsDialog) selectedProjectID() string {
	if len(d.projects) == 0 {
		return ""
	}
	idx := int(d.detailProjectRow.Selected())
	if idx < 0 || idx >= len(d.projects) {
		return ""
	}
	return d.projects[idx].ID
}

func (d *VarsetsDialog) projectIndex(id string) uint {
	for i, p := range d.projects {
		if p.ID == id {
			return uint(i)
		}
	}
	return 0
}

func scopeToIndex(s domain.VariableScope) uint {
	switch s {
	case domain.ScopeProject:
		return 1
	case domain.ScopeWorkspace:
		return 2
	}
	return 0 // Global
}

func indexToScope(i uint) domain.VariableScope {
	switch i {
	case 1:
		return domain.ScopeProject
	case 2:
		return domain.ScopeWorkspace
	}
	return domain.ScopeGlobal
}

func (d *VarsetsDialog) populateDetailVars(set domain.VariableSet) {
	clearGroup(d.detailVarsGrp)
	if len(set.Variables) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No variables yet")
		empty.SetSubtitle("Use the + button above to add one.")
		empty.AddCSSClass("dim-label")
		d.detailVarsGrp.Add(empty)
		return
	}
	for _, v := range set.Variables {
		row := buildSetVarRow(v)
		v := v
		row.ConnectActivated(func() { d.editVar(v) })
		d.detailVarsGrp.Add(row)
	}
}

func buildSetVarRow(v domain.Variable) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(escapeMarkup(v.Key))
	row.SetActivatable(true)

	display := "—"
	switch {
	case v.Sensitive:
		display = "•••••"
	case v.Value != "":
		display = truncate(v.Value, 60)
	}
	row.SetSubtitle(escapeMarkup(display))

	if v.Sensitive {
		badge := gtk.NewLabel("sensitive")
		badge.AddCSSClass("pill")
		badge.AddCSSClass("warning")
		badge.SetVAlign(gtk.AlignCenter)
		row.AddSuffix(badge)
	} else if v.Category == domain.VarCategoryEnvironment {
		badge := gtk.NewLabel("env")
		badge.AddCSSClass("pill")
		badge.AddCSSClass("accent")
		badge.SetVAlign(gtk.AlignCenter)
		row.AddSuffix(badge)
	}
	return row
}

func (d *VarsetsDialog) onCreate() {
	// Quick-create with a placeholder name; the user renames in detail.
	set, err := d.backend.CreateVariableSet(context.Background(), "Untitled Set", "")
	if err != nil {
		slog.Error("create varset", "err", err)
		return
	}
	slog.Info("varset created", "id", set.ID, "name", set.Name)
	d.refreshList()
	d.openDetail(set)
}

func (d *VarsetsDialog) onAddVar() {
	if d.current.ID == "" {
		return
	}
	parent, _ := d.dialog.Parent().(*gtk.Window)
	EditVariable(parent, VarEditAdd, domain.Variable{}, d.saveVar)
}

func (d *VarsetsDialog) editVar(v domain.Variable) {
	parent, _ := d.dialog.Parent().(*gtk.Window)
	EditVariable(parent, VarEditEdit, v, d.saveVar)
}

func (d *VarsetsDialog) saveVar(v domain.Variable) {
	if d.current.ID == "" {
		return
	}
	if err := d.backend.UpsertVariableSetVar(context.Background(), d.current.ID, v); err != nil {
		slog.Error("upsert varset var", "set", d.current.ID, "key", v.Key, "err", err)
		return
	}
	updated, err := d.backend.VariableSet(context.Background(), d.current.ID)
	if err == nil {
		d.current = updated
		d.populateDetailVars(updated)
	}
	d.refreshList()
}

func (d *VarsetsDialog) onDelete() {
	if d.current.ID == "" {
		return
	}
	id := d.current.ID
	if err := d.backend.DeleteVariableSet(context.Background(), id); err != nil {
		slog.Error("delete varset", "id", id, "err", err)
		return
	}
	slog.Info("varset deleted", "id", id)
	d.current = domain.VariableSet{}
	d.refreshList()
	d.nav.PopToTag("list")
}

func clearGroup(g *adw.PreferencesGroup) {
	for {
		// Workaround: AdwPreferencesGroup doesn't expose a "remove all"
		// helper. We walk the underlying ListBox the group hosts.
		row := g.FirstChild()
		if row == nil {
			return
		}
		// The group's first child is its internal Box; we want children of
		// that Box. Use Adw's RemoveCSS pattern via Remove(child) on the
		// AdwPreferencesGroup itself which DOES exist.
		if rmer, ok := any(g).(interface{ Remove(child gtk.Widgetter) }); ok {
			rmer.Remove(row)
			continue
		}
		// Fallback: bail out if the API surface ever changes — better than
		// looping forever.
		return
	}
}

func pluralize(n int, label string) string {
	if n == 1 {
		return "1 " + label
	}
	return strings.TrimRight(itoa(n)+" "+label+"s", " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	negative := n < 0
	if negative {
		n = -n
	}
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

// truncate is duplicated from widgets.truncate to avoid an import cycle.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// escapeMarkup is duplicated from widgets.escapeMarkup for the same reason.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
