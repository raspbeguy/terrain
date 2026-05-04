package widgets

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
)

// VarList renders the discovered variables of a workspace as a list of
// AdwActionRow widgets. Rows are activatable when an OnActivate callback is
// installed — clicking opens the edit dialog upstream. A kebab suffix adds
// per-row actions (Remove) when OnRemove is installed.
type VarList struct {
	scroller *gtk.ScrolledWindow
	body     *gtk.Box

	status *adw.StatusPage

	onActivate func(domain.Variable)
	onRemove   func(domain.Variable)
}

// NewVarList builds the widget showing an empty placeholder until Bind.
func NewVarList() *VarList {
	body := gtk.NewBox(gtk.OrientationVertical, 0)
	body.SetHExpand(true)
	body.SetVExpand(true)

	status := adw.NewStatusPage()
	status.SetIconName("document-properties-symbolic")
	status.SetTitle("No variables")
	status.SetDescription("This workspace doesn't declare any input variables.")
	status.SetVExpand(true)
	body.Append(status)

	scroller := gtk.NewScrolledWindow()
	scroller.SetHExpand(true)
	scroller.SetVExpand(true)
	scroller.SetChild(body)

	return &VarList{scroller: scroller, body: body, status: status}
}

// Root returns the top-level widget for embedding.
func (vl *VarList) Root() *gtk.ScrolledWindow { return vl.scroller }

// SetOnActivate registers the callback fired when the user clicks a row.
// Pass nil to make the list non-interactive.
func (vl *VarList) SetOnActivate(fn func(domain.Variable)) { vl.onActivate = fn }

// SetOnRemove installs the kebab Remove callback; nil hides the kebab.
func (vl *VarList) SetOnRemove(fn func(domain.Variable)) { vl.onRemove = fn }

// Bind replaces the current view with the supplied list of variables. Empty
// list shows the placeholder.
func (vl *VarList) Bind(vars []domain.Variable) {
	vl.clear()
	if len(vars) == 0 {
		vl.body.Append(vl.status)
		return
	}
	page := adw.NewPreferencesPage()

	declared := adw.NewPreferencesGroup()
	declared.SetTitle("Workspace Variables")
	for _, v := range vars {
		declared.Add(vl.buildActivatableRow(v))
	}
	page.Add(declared)

	vl.body.Append(page)
}

func (vl *VarList) buildActivatableRow(v domain.Variable) *adw.ActionRow {
	row := buildVarRow(v)
	if vl.onActivate != nil {
		row.SetActivatable(true)
		v := v
		row.ConnectActivated(func() { vl.onActivate(v) })
	}
	if vl.onRemove != nil {
		row.AddSuffix(buildVarRowKebab(v, vl.onRemove))
	}
	return row
}

// buildVarRowKebab returns a MenuButton with a popover offering Remove
// (or "Reset to default" for source-declared variables).
func buildVarRowKebab(v domain.Variable, onRemove func(domain.Variable)) *gtk.MenuButton {
	popover := gtk.NewPopover()
	popover.SetHasArrow(true)

	label := "Remove"
	if v.Declared {
		label = "Reset to default"
	}
	btn := gtk.NewButtonWithLabel(label)
	btn.AddCSSClass("flat")
	btn.AddCSSClass("destructive-action")
	btn.ConnectClicked(func() {
		popover.Popdown()
		onRemove(v)
	})
	popover.SetChild(btn)

	menu := gtk.NewMenuButton()
	menu.SetIconName("view-more-symbolic")
	menu.AddCSSClass("flat")
	menu.SetVAlign(gtk.AlignCenter)
	menu.SetPopover(popover)
	menu.SetTooltipText("More actions")
	return menu
}

// SetError replaces the view with an error placeholder.
func (vl *VarList) SetError(message string) {
	vl.clear()
	vl.status.SetIconName("dialog-error-symbolic")
	vl.status.SetTitle("Couldn't load variables")
	vl.status.SetDescription(message)
	vl.body.Append(vl.status)
}

func (vl *VarList) clear() {
	for child := vl.body.FirstChild(); child != nil; child = vl.body.FirstChild() {
		vl.body.Remove(child)
	}
}

func buildVarRow(v domain.Variable) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(escapeMarkup(v.Key))

	subtitle := v.Description
	if subtitle == "" {
		subtitle = "—"
	}
	row.SetSubtitle(escapeMarkup(subtitle))

	value := varDisplay(v)
	badge := gtk.NewLabel(value)
	badge.AddCSSClass("dim-label")
	badge.SetVAlign(gtk.AlignCenter)
	badge.SetSelectable(true)
	row.AddSuffix(badge)

	if v.Sensitive {
		sensBadge := gtk.NewLabel("sensitive")
		sensBadge.AddCSSClass("pill")
		sensBadge.AddCSSClass("warning")
		sensBadge.SetVAlign(gtk.AlignCenter)
		row.AddSuffix(sensBadge)
	}
	if v.Category == domain.VarCategoryEnvironment {
		envBadge := gtk.NewLabel("env")
		envBadge.AddCSSClass("pill")
		envBadge.AddCSSClass("accent")
		envBadge.SetVAlign(gtk.AlignCenter)
		row.AddSuffix(envBadge)
	}
	// Ad-hoc: tfvars entry with no matching `variable` block in source.
	// Env-category gets the "env" pill instead, so don't double-flag.
	if !v.Declared && v.Category != domain.VarCategoryEnvironment {
		adhoc := gtk.NewLabel("ad-hoc")
		adhoc.AddCSSClass("pill")
		adhoc.SetVAlign(gtk.AlignCenter)
		adhoc.SetTooltipText("No matching `variable` block in this workspace's .tf files")
		row.AddSuffix(adhoc)
	}
	return row
}

// varDisplay returns the row's suffix label: bullets for sensitive, a
// truncated single-line render for plain (HCL multi-line literals get
// flattened to one line first), dash for empty.
func varDisplay(v domain.Variable) string {
	switch {
	case v.Sensitive:
		return "•••••"
	case v.Value != "":
		return truncate(strings.Join(strings.Fields(v.Value), " "), 60)
	}
	return "—"
}
