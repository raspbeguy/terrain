package widgets

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"
)

// PlanDiff renders parsed plan changes as expander rows. Built on
// AdwPreferencesPage rather than a virtualized list — fine up to ~1k
// resources; revisit if real plans get bigger.
type PlanDiff struct {
	scroller   *gtk.ScrolledWindow
	body       *gtk.Box
	status     *adw.StatusPage
	currentGrp *adw.PreferencesGroup
	page       *adw.PreferencesPage
}

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

func (pd *PlanDiff) Root() *gtk.ScrolledWindow { return pd.scroller }

// Bind replaces the current view; nil or no-change plans show the empty state.
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

	for _, attrRow := range buildAttributeRows(rc) {
		row.AddRow(attrRow)
	}
	return row
}

// buildAttributeRows: top-level attributes only; nested values need a real diff walker.
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
		row.AddCSSClass("property")
		rows = append(rows, row)
	}
	return rows
}

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
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
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

// escapeMarkup hides Pango metacharacters; resource addresses can contain `<`.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
