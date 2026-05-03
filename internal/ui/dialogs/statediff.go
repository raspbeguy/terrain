package dialogs

import (
	"context"
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
	"github.com/raspbeguy/terrain/internal/ui/widgets"
)

const stateDiffResource = "/io/github/raspbeguy/Terrain/state-diff.ui"

// StateDiffLoader is the subset of backend operations the dialog needs to
// fetch state JSON for the two selected versions. Defined as a callback
// type so the window can route to the appropriate backend without leaking
// the backend type across the package boundary.
type StateDiffLoader = func(versionID string) (*tfjson.State, error)

// PresentStateDiff opens the compare-versions dialog. versions is the list
// of available snapshots (newest first) plus a synthetic "Live" entry that
// the loader handles for ID == "" (live state). loader is called every
// time the user changes either combo to refetch and re-diff.
func PresentStateDiff(parent *gtk.Window, versions []domain.StateVersion, loader StateDiffLoader) {
	if loader == nil {
		slog.Warn("state diff: no loader provided")
		return
	}
	d := newStateDiffDialog(versions, loader)
	d.dialog.Present(parent)
}

type stateDiffDialog struct {
	dialog    *adw.Dialog
	fromRow   *adw.ComboRow
	toRow     *adw.ComboRow
	container *adw.Bin

	diff *widgets.StateDiff

	versions []domain.StateVersion // index 0 maps to combo index 1; combo 0 is "Live"
	loader   StateDiffLoader

	suppress bool
}

func newStateDiffDialog(versions []domain.StateVersion, loader StateDiffLoader) *stateDiffDialog {
	builder := gtk.NewBuilderFromResource(stateDiffResource)
	d := &stateDiffDialog{
		dialog:    uihelpers.MustCast[*adw.Dialog](builder, "statediff_dialog"),
		fromRow:   uihelpers.MustCast[*adw.ComboRow](builder, "statediff_from_row"),
		toRow:     uihelpers.MustCast[*adw.ComboRow](builder, "statediff_to_row"),
		container: uihelpers.MustCast[*adw.Bin](builder, "statediff_container"),
		versions:  versions,
		loader:    loader,
		diff:      widgets.NewStateDiff(),
	}
	d.container.SetChild(d.diff.Root())

	d.populateCombos()

	d.fromRow.Connect("notify::selected", func() { d.recompute() })
	d.toRow.Connect("notify::selected", func() { d.recompute() })

	// Default selection: Live (index 0) → most recent snapshot (index 1 if
	// any). Triggers an initial compute when From flips.
	d.suppress = true
	d.fromRow.SetSelected(0)
	if len(versions) > 0 {
		d.toRow.SetSelected(1)
	} else {
		d.toRow.SetSelected(0)
	}
	d.suppress = false
	d.recompute()

	return d
}

func (d *stateDiffDialog) populateCombos() {
	model := gtk.NewStringList(nil)
	model.Append("Live (current)")
	for _, v := range d.versions {
		model.Append(v.CreatedAt.Format("2006-01-02 15:04") + "  ·  serial " + intToStr(v.Serial))
	}
	d.fromRow.SetModel(model)
	d.toRow.SetModel(model)
}

func (d *stateDiffDialog) recompute() {
	if d.suppress {
		return
	}
	fromState, err := d.fetch(int(d.fromRow.Selected()))
	if err != nil {
		d.diff.SetError(err.Error())
		return
	}
	toState, err := d.fetch(int(d.toRow.Selected()))
	if err != nil {
		d.diff.SetError(err.Error())
		return
	}
	d.diff.Bind(fromState, toState)
}

func (d *stateDiffDialog) fetch(comboIndex int) (*tfjson.State, error) {
	if comboIndex == 0 {
		// Live entry — empty version ID signals "current state" to the
		// loader.
		return d.loader("")
	}
	idx := comboIndex - 1
	if idx < 0 || idx >= len(d.versions) {
		return nil, nil
	}
	return d.loader(d.versions[idx].ID)
}

// intToStr is a tiny helper used by the version label format. Mirrors the
// itoa in workspace.Page so we don't pull strconv into this file.
func intToStr(n int64) string {
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

// Compile-time check the loader signature matches the type alias.
var _ context.Context = context.Background()
