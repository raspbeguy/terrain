package widgets

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
)

// VarList renders the discovered variables of a workspace as a list of
// AdwActionRow widgets. Rows are activatable when an OnActivate callback is
// installed — clicking opens the edit dialog upstream.
type VarList struct {
	scroller *gtk.ScrolledWindow
	body     *gtk.Box

	status *adw.StatusPage

	onActivate func(domain.Variable)
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
	return row
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
	return row
}

// varDisplay picks the most informative single-line representation: hide
// sensitive values, otherwise flatten any whitespace runs (HCL object/map
// literals come through multi-line) and truncate. Empty values render as
// a dash.
func varDisplay(v domain.Variable) string {
	switch {
	case v.Sensitive:
		return "•••••"
	case v.Value != "":
		return truncate(strings.Join(strings.Fields(v.Value), " "), 60)
	}
	return "—"
}
