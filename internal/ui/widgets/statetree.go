package widgets

import (
	"fmt"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"
)

// StateTree renders a parsed Terraform/OpenTofu state. Each resource is an
// AdwExpanderRow titled by its address; expanding shows its attributes as
// child action rows. Modules nest under their parent.
//
// Like PlanDiff, this is a simple AdwPreferencesPage rather than a
// virtualized GtkColumnView — fine for the typical low-thousands resource
// counts we expect locally.
type StateTree struct {
	scroller *gtk.ScrolledWindow
	body     *gtk.Box

	status *adw.StatusPage
}

// NewStateTree creates an empty state tree showing a placeholder until the
// first Bind call.
func NewStateTree() *StateTree {
	body := gtk.NewBox(gtk.OrientationVertical, 0)
	body.SetHExpand(true)
	body.SetVExpand(true)

	status := adw.NewStatusPage()
	status.SetIconName("view-grid-symbolic")
	status.SetTitle("No state loaded")
	status.SetDescription("Click Refresh to load the current state of this workspace.")
	status.SetVExpand(true)
	body.Append(status)

	scroller := gtk.NewScrolledWindow()
	scroller.SetHExpand(true)
	scroller.SetVExpand(true)
	scroller.SetChild(body)

	return &StateTree{
		scroller: scroller,
		body:     body,
		status:   status,
	}
}

// Root returns the top-level widget for embedding.
func (st *StateTree) Root() *gtk.ScrolledWindow { return st.scroller }

// Bind replaces the current view with a tree built from state. nil shows
// the empty placeholder; an empty state shows the "no resources" page.
func (st *StateTree) Bind(state *tfjson.State) {
	st.clear()

	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		st.body.Append(st.status)
		return
	}

	page := adw.NewPreferencesPage()
	rootMod := state.Values.RootModule

	resourceCount := countResources(rootMod)
	if resourceCount == 0 {
		st.status.SetTitle("Empty state")
		st.status.SetDescription("This workspace doesn't have any resources yet.")
		st.body.Append(st.status)
		return
	}

	summary := adw.NewPreferencesGroup()
	summary.SetTitle("Summary")
	summary.SetDescription(fmt.Sprintf("%d resource%s", resourceCount, plural(resourceCount)))
	page.Add(summary)

	resourcesGrp := adw.NewPreferencesGroup()
	resourcesGrp.SetTitle("Resources")
	for _, r := range rootMod.Resources {
		resourcesGrp.Add(buildStateResourceRow(r))
	}
	page.Add(resourcesGrp)

	for _, child := range rootMod.ChildModules {
		page.Add(buildModuleGroup(child))
	}

	st.body.Append(page)
}

// SetError shows an error message in place of the tree. Used when loading
// state fails (e.g. tofu show -json fails because the backend isn't
// initialized).
func (st *StateTree) SetError(message string) {
	st.clear()
	st.status.SetTitle("Couldn't load state")
	st.status.SetDescription(message)
	st.status.SetIconName("dialog-error-symbolic")
	st.body.Append(st.status)
}

func (st *StateTree) clear() {
	for child := st.body.FirstChild(); child != nil; child = st.body.FirstChild() {
		st.body.Remove(child)
	}
}

func buildStateResourceRow(r *tfjson.StateResource) *adw.ExpanderRow {
	row := adw.NewExpanderRow()
	row.SetTitle(escapeMarkup(r.Address))
	if r.ProviderName != "" {
		row.SetSubtitle(escapeMarkup(r.ProviderName))
	}

	badge := gtk.NewLabel(string(r.Mode))
	badge.AddCSSClass("dim-label")
	badge.SetVAlign(gtk.AlignCenter)
	row.AddSuffix(badge)

	attrs := r.AttributeValues
	if attrs == nil {
		attrs = map[string]any{}
	}
	for _, k := range mapKeysSorted(attrs) {
		av := attrs[k]
		attrRow := adw.NewActionRow()
		attrRow.SetTitle(escapeMarkup(k))
		attrRow.SetSubtitle(escapeMarkup(truncate(jsonOf(av), 96)))
		attrRow.AddCSSClass("property")
		row.AddRow(attrRow)
	}
	return row
}

func buildModuleGroup(m *tfjson.StateModule) *adw.PreferencesGroup {
	grp := adw.NewPreferencesGroup()
	grp.SetTitle(escapeMarkup(m.Address))
	grp.SetDescription(fmt.Sprintf("%d resource%s",
		len(m.Resources), plural(len(m.Resources))))
	for _, r := range m.Resources {
		grp.Add(buildStateResourceRow(r))
	}
	return grp
}

func countResources(m *tfjson.StateModule) int {
	if m == nil {
		return 0
	}
	n := len(m.Resources)
	for _, c := range m.ChildModules {
		n += countResources(c)
	}
	return n
}

func mapKeysSorted(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
