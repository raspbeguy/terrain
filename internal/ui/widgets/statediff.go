package widgets

import (
	"encoding/json"
	"fmt"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"
)

// StateDiff renders the difference between two parsed states. Each changed
// resource is an AdwExpanderRow with an action badge (added/changed/
// removed); expanding shows attribute-level before/after pairs.
//
// Algorithm:
//   - Index resources by address from both states (recursive across modules)
//   - In-both with differing attributes → "changed"
//   - In to-only → "added"
//   - In from-only → "removed"
//   - In both with identical attributes → no-op (skipped)
type StateDiff struct {
	scroller *gtk.ScrolledWindow
	body     *gtk.Box
	status   *adw.StatusPage
}

// NewStateDiff builds an empty diff view showing a placeholder.
func NewStateDiff() *StateDiff {
	body := gtk.NewBox(gtk.OrientationVertical, 0)
	body.SetHExpand(true)
	body.SetVExpand(true)

	status := adw.NewStatusPage()
	status.SetIconName("view-list-symbolic")
	status.SetTitle("Pick two versions")
	status.SetDescription("Select a from / to pair above to see what changed.")
	status.SetVExpand(true)
	body.Append(status)

	scroller := gtk.NewScrolledWindow()
	scroller.SetHExpand(true)
	scroller.SetVExpand(true)
	scroller.SetChild(body)

	return &StateDiff{scroller: scroller, body: body, status: status}
}

// Root returns the top-level widget for embedding.
func (sd *StateDiff) Root() *gtk.ScrolledWindow { return sd.scroller }

// Bind replaces the current view with the diff between from and to. Pass
// nil for either to show the placeholder; identical states show a "no
// differences" message.
func (sd *StateDiff) Bind(from, to *tfjson.State) {
	sd.clear()

	if from == nil || to == nil {
		sd.body.Append(sd.status)
		return
	}

	added, changed, removed := diffStates(from, to)

	page := adw.NewPreferencesPage()

	summary := adw.NewPreferencesGroup()
	summary.SetTitle("Summary")
	summary.SetDescription(fmt.Sprintf(
		"%d added · %d changed · %d removed",
		len(added), len(changed), len(removed),
	))
	page.Add(summary)

	if len(added)+len(changed)+len(removed) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No differences")
		empty.SetSubtitle("The two snapshots are identical.")
		empty.AddCSSClass("dim-label")
		summary.Add(empty)
		sd.body.Append(page)
		return
	}

	if len(added) > 0 {
		g := adw.NewPreferencesGroup()
		g.SetTitle("Added")
		for _, r := range added {
			g.Add(buildSimpleResourceRow(r.Address, r.ProviderName, "+", "success"))
		}
		page.Add(g)
	}
	if len(changed) > 0 {
		g := adw.NewPreferencesGroup()
		g.SetTitle("Changed")
		for _, r := range changed {
			g.Add(buildChangedResourceRow(r))
		}
		page.Add(g)
	}
	if len(removed) > 0 {
		g := adw.NewPreferencesGroup()
		g.SetTitle("Removed")
		for _, r := range removed {
			g.Add(buildSimpleResourceRow(r.Address, r.ProviderName, "−", "error"))
		}
		page.Add(g)
	}

	sd.body.Append(page)
}

// SetError replaces the view with an error placeholder.
func (sd *StateDiff) SetError(msg string) {
	sd.clear()
	sd.status.SetIconName("dialog-error-symbolic")
	sd.status.SetTitle("Couldn't compute diff")
	sd.status.SetDescription(msg)
	sd.body.Append(sd.status)
}

func (sd *StateDiff) clear() {
	for child := sd.body.FirstChild(); child != nil; child = sd.body.FirstChild() {
		sd.body.Remove(child)
	}
}

// resourceDiff is the internal carrier between the diff algorithm and the
// row builders. Address is the terraform-style address (e.g. "module.x.aws_instance.y[0]");
// AttrChanges is non-nil only for "changed" entries.
type resourceDiff struct {
	Address      string
	ProviderName string
	From         *tfjson.StateResource // nil when added
	To           *tfjson.StateResource // nil when removed
	AttrChanges  []attrChange
}

type attrChange struct {
	Key        string
	From       any
	To         any
	FromExists bool
	ToExists   bool
}

func diffStates(from, to *tfjson.State) (added, changed, removed []resourceDiff) {
	fromIdx := indexStateResources(from)
	toIdx := indexStateResources(to)

	for addr, tr := range toIdx {
		fr, ok := fromIdx[addr]
		if !ok {
			added = append(added, resourceDiff{Address: addr, ProviderName: tr.ProviderName, To: tr})
			continue
		}
		attrs := diffStateAttrs(fr.AttributeValues, tr.AttributeValues)
		if len(attrs) == 0 {
			continue
		}
		changed = append(changed, resourceDiff{
			Address:      addr,
			ProviderName: tr.ProviderName,
			From:         fr,
			To:           tr,
			AttrChanges:  attrs,
		})
	}
	for addr, fr := range fromIdx {
		if _, ok := toIdx[addr]; ok {
			continue
		}
		removed = append(removed, resourceDiff{Address: addr, ProviderName: fr.ProviderName, From: fr})
	}
	sortDiffsByAddress(added)
	sortDiffsByAddress(changed)
	sortDiffsByAddress(removed)
	return
}

// indexStateResources flattens a state's nested module tree into an
// address-keyed map. The address terraform uses (e.g. "module.x.aws_instance.y")
// is unique across the tree.
func indexStateResources(s *tfjson.State) map[string]*tfjson.StateResource {
	out := make(map[string]*tfjson.StateResource)
	if s == nil || s.Values == nil {
		return out
	}
	walkStateModule(s.Values.RootModule, out)
	return out
}

func walkStateModule(m *tfjson.StateModule, out map[string]*tfjson.StateResource) {
	if m == nil {
		return
	}
	for _, r := range m.Resources {
		out[r.Address] = r
	}
	for _, child := range m.ChildModules {
		walkStateModule(child, out)
	}
}

// diffStateAttrs returns the per-key changes between two attribute maps.
// Skip when both encode to the same JSON; otherwise emit an attrChange
// with whether each side had the key set.
func diffStateAttrs(a, b map[string]any) []attrChange {
	keys := mergedKeys(a, b)
	out := make([]attrChange, 0, len(keys))
	for _, k := range keys {
		av, aOk := a[k]
		bv, bOk := b[k]
		if equalJSON(av, bv) {
			continue
		}
		out = append(out, attrChange{
			Key:        k,
			From:       av,
			To:         bv,
			FromExists: aOk,
			ToExists:   bOk,
		})
	}
	return out
}

func sortDiffsByAddress(d []resourceDiff) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j-1].Address > d[j].Address; j-- {
			d[j-1], d[j] = d[j], d[j-1]
		}
	}
}

func buildSimpleResourceRow(address, provider, glyph, cssClass string) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(escapeMarkup(address))
	if provider != "" {
		row.SetSubtitle(escapeMarkup(provider))
	}

	badge := gtk.NewLabel(glyph)
	badge.AddCSSClass("pill")
	badge.AddCSSClass(cssClass)
	badge.SetVAlign(gtk.AlignCenter)
	row.AddSuffix(badge)
	return row
}

func buildChangedResourceRow(r resourceDiff) *adw.ExpanderRow {
	row := adw.NewExpanderRow()
	row.SetTitle(escapeMarkup(r.Address))
	if r.ProviderName != "" {
		row.SetSubtitle(escapeMarkup(r.ProviderName))
	}

	badge := gtk.NewLabel("~")
	badge.AddCSSClass("pill")
	badge.AddCSSClass("accent")
	badge.SetVAlign(gtk.AlignCenter)
	row.AddSuffix(badge)

	for _, c := range r.AttrChanges {
		attrRow := adw.NewActionRow()
		attrRow.SetTitle(escapeMarkup(c.Key))
		var sub string
		switch {
		case !c.FromExists:
			sub = "+ " + truncate(jsonOf(c.To), 96)
		case !c.ToExists:
			sub = "- " + truncate(jsonOf(c.From), 96)
		default:
			sub = truncate(jsonOf(c.From), 48) + "  →  " + truncate(jsonOf(c.To), 48)
		}
		attrRow.SetSubtitle(escapeMarkup(sub))
		attrRow.AddCSSClass("property")
		row.AddRow(attrRow)
	}
	return row
}

// equalJSON / mergedKeys / jsonOf / truncate / escapeMarkup are shared with
// plandiff.go in the same package.
var _ = json.Marshal // keep encoding/json reachable for future expansion
