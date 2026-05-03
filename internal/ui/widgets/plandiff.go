package widgets

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"
)

// PlanDiff renders the per-resource changes of a parsed plan. Each resource
// gets an AdwExpanderRow with an action badge (+/~/-/-/+) in the title; the
// expanded body shows attribute-level before/after pairs.
//
// Backed by an AdwPreferencesPage rather than a virtualized list because
// the typical plan touches under a thousand resources and the simpler
// AdwExpanderRow tree gives us free keyboard navigation and theming.
// If we ever see real-world plans with 10k+ changes we'll migrate to
// GtkColumnView + GtkTreeListModel.
type PlanDiff struct {
	scroller *gtk.ScrolledWindow
	body     *gtk.Box

	// stack toggles between an empty-state status page and the populated
	// diff. We track the live group so Bind can replace it cleanly.
	status     *adw.StatusPage
	currentGrp *adw.PreferencesGroup
	page       *adw.PreferencesPage
}

// NewPlanDiff builds an empty diff view.
func NewPlanDiff() *PlanDiff {
	page := adw.NewPreferencesPage()

	body := gtk.NewBox(gtk.OrientationVertical, 0)
	body.SetHExpand(true)
	body.SetVExpand(true)

	status := adw.NewStatusPage()
	status.SetIconName("view-list-bullet-symbolic")
	status.SetTitle("No plan yet")
	status.SetDescription("Run a plan to see resource changes here.")
	status.SetVExpand(true)

	body.Append(status)

	scroller := gtk.NewScrolledWindow()
	scroller.SetHExpand(true)
	scroller.SetVExpand(true)
	scroller.SetChild(body)

	return &PlanDiff{
		scroller: scroller,
		body:     body,
		status:   status,
		page:     page,
	}
}

// Root returns the top-level widget for embedding.
func (pd *PlanDiff) Root() *gtk.ScrolledWindow { return pd.scroller }

// Bind replaces the current view with the diff of the provided plan. Pass
// nil (or a plan with no changes) to show the empty state.
func (pd *PlanDiff) Bind(plan *tfjson.Plan) {
	pd.clear()
	if plan == nil {
		pd.body.Append(pd.status)
		return
	}

	add, change, destroy := tallyActions(plan)
	if add+change+destroy == 0 {
		pd.status.SetTitle("No changes")
		pd.status.SetDescription("Resources are already in sync with the configuration.")
		pd.body.Append(pd.status)
		return
	}

	pd.page = adw.NewPreferencesPage()

	summary := adw.NewPreferencesGroup()
	summary.SetTitle("Plan Summary")
	summary.SetDescription(fmt.Sprintf("%d to add, %d to change, %d to destroy", add, change, destroy))
	pd.page.Add(summary)

	resources := adw.NewPreferencesGroup()
	resources.SetTitle("Resource Changes")
	for _, rc := range plan.ResourceChanges {
		if isNoOp(rc.Change.Actions) {
			continue
		}
		resources.Add(buildResourceRow(rc))
	}
	pd.page.Add(resources)
	pd.currentGrp = resources

	pd.body.Append(pd.page)
}

func (pd *PlanDiff) clear() {
	for child := pd.body.FirstChild(); child != nil; child = pd.body.FirstChild() {
		pd.body.Remove(child)
	}
}

// buildResourceRow renders one resource change as an expander row.
func buildResourceRow(rc *tfjson.ResourceChange) *adw.ExpanderRow {
	row := adw.NewExpanderRow()
	row.SetTitle(escapeMarkup(rc.Address))
	if rc.ProviderName != "" {
		row.SetSubtitle(escapeMarkup(rc.ProviderName))
	}

	badge := gtk.NewLabel(actionSymbol(rc.Change.Actions))
	badge.AddCSSClass("pill")
	badge.AddCSSClass(actionCSSClass(rc.Change.Actions))
	badge.SetVAlign(gtk.AlignCenter)
	row.AddSuffix(badge)

	// Populate inner rows for the attribute diffs we can extract.
	for _, attrRow := range buildAttributeRows(rc) {
		row.AddRow(attrRow)
	}
	return row
}

// buildAttributeRows produces an AdwActionRow per top-level attribute that
// changed between Before and After. We use top-level only because
// recursively rendering nested HCL/JSON values reasonably is a separate
// concern (lands when we have a proper diff library or cty.Value walker).
func buildAttributeRows(rc *tfjson.ResourceChange) []*adw.ActionRow {
	before, _ := rc.Change.Before.(map[string]any)
	after, _ := rc.Change.After.(map[string]any)
	if before == nil && after == nil {
		return nil
	}

	keys := mergedKeys(before, after)
	rows := make([]*adw.ActionRow, 0, len(keys))
	for _, k := range keys {
		bv, beforeOK := before[k]
		av, afterOK := after[k]
		if equalJSON(bv, av) {
			continue
		}

		row := adw.NewActionRow()
		row.SetTitle(escapeMarkup(k))
		var sub string
		switch {
		case !beforeOK:
			sub = "+ " + truncate(jsonOf(av), 96)
		case !afterOK:
			sub = "- " + truncate(jsonOf(bv), 96)
		default:
			sub = truncate(jsonOf(bv), 48) + "  →  " + truncate(jsonOf(av), 48)
		}
		row.SetSubtitle(escapeMarkup(sub))
		// AdwActionRow doesn't theme its subtitle by default; we use a tiny
		// monospace badge style for the value pairs.
		row.AddCSSClass("property")
		rows = append(rows, row)
	}
	return rows
}

// tallyActions returns (add, change, destroy) counts across the plan.
func tallyActions(plan *tfjson.Plan) (add, change, destroy int) {
	for _, rc := range plan.ResourceChanges {
		switch {
		case has(rc.Change.Actions, tfjson.ActionCreate) && has(rc.Change.Actions, tfjson.ActionDelete):
			add++
			destroy++
		case has(rc.Change.Actions, tfjson.ActionCreate):
			add++
		case has(rc.Change.Actions, tfjson.ActionUpdate):
			change++
		case has(rc.Change.Actions, tfjson.ActionDelete):
			destroy++
		}
	}
	return
}

func has(actions tfjson.Actions, want tfjson.Action) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

func isNoOp(actions tfjson.Actions) bool {
	return len(actions) == 1 && actions[0] == tfjson.ActionNoop
}

// actionSymbol picks a single-character glyph for the resource header.
func actionSymbol(actions tfjson.Actions) string {
	switch {
	case has(actions, tfjson.ActionCreate) && has(actions, tfjson.ActionDelete):
		return "−/+"
	case has(actions, tfjson.ActionCreate):
		return "+"
	case has(actions, tfjson.ActionUpdate):
		return "~"
	case has(actions, tfjson.ActionDelete):
		return "−"
	case has(actions, tfjson.ActionRead):
		return "?"
	}
	return ""
}

// actionCSSClass picks the libadwaita-themed colour class for the badge.
func actionCSSClass(actions tfjson.Actions) string {
	switch {
	case has(actions, tfjson.ActionCreate) && has(actions, tfjson.ActionDelete):
		return "warning"
	case has(actions, tfjson.ActionCreate):
		return "success"
	case has(actions, tfjson.ActionUpdate):
		return "accent"
	case has(actions, tfjson.ActionDelete):
		return "error"
	}
	return "dim-label"
}

func mergedKeys(a, b map[string]any) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	// stable order so the row layout doesn't shuffle on rebuild
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	// tiny insertion sort to avoid pulling in sort just for this
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func jsonOf(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// escapeMarkup hides Pango markup metacharacters in user-controlled strings;
// resource addresses can in theory contain `<` etc.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
