package dialogs

import (
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/local"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const wsSettingsResource = "/io/github/raspbeguy/Terrain/workspace-settings.ui"

type WorkspaceSettings struct {
	dialog *adw.PreferencesDialog

	runModeRow *adw.ComboRow
	imageRow   *adw.EntryRow

	backendID   string
	workspaceID string
	// initial is the on-disk snapshot; persist() compares against it so a
	// no-op edit doesn't cause a needless write.
	initial local.WorkspaceSettings
}

func NewWorkspaceSettings(backendID, workspaceID string) *WorkspaceSettings {
	builder := gtk.NewBuilderFromResource(wsSettingsResource)
	w := &WorkspaceSettings{
		dialog:      uihelpers.MustCast[*adw.PreferencesDialog](builder, "ws_settings_dialog"),
		runModeRow:  uihelpers.MustCast[*adw.ComboRow](builder, "ws_run_mode_row"),
		imageRow:    uihelpers.MustCast[*adw.EntryRow](builder, "ws_image_row"),
		backendID:   backendID,
		workspaceID: workspaceID,
	}

	current, err := local.LoadWorkspaceSettings(backendID, workspaceID)
	if err != nil {
		slog.Warn("load workspace settings", "ws", workspaceID, "err", err)
	}
	w.initial = current
	switch current.RunMode {
	case local.RunModeSubprocess:
		w.runModeRow.SetSelected(1)
	case local.RunModeBubblewrap:
		w.runModeRow.SetSelected(2)
	case local.RunModeContainer:
		w.runModeRow.SetSelected(3)
	default:
		w.runModeRow.SetSelected(0)
	}
	w.imageRow.SetText(current.Image)

	w.runModeRow.Connect("notify::selected", w.persist)
	w.imageRow.ConnectApply(w.persist)

	return w
}

func (w *WorkspaceSettings) Present(parent *gtk.Window) {
	w.dialog.Present(parent)
}

func (w *WorkspaceSettings) persist() {
	want := local.WorkspaceSettings{
		RunMode: w.selectedRunMode(),
		Image:   w.imageRow.Text(),
	}
	if want == w.initial {
		return
	}
	if err := local.SaveWorkspaceSettings(w.backendID, w.workspaceID, want); err != nil {
		slog.Error("save workspace settings", "ws", w.workspaceID, "err", err)
		return
	}
	w.initial = want
	slog.Info("workspace settings saved",
		"ws", w.workspaceID,
		"run_mode", want.RunMode,
		"image", want.Image)
}

func (w *WorkspaceSettings) selectedRunMode() local.RunMode {
	switch w.runModeRow.Selected() {
	case 1:
		return local.RunModeSubprocess
	case 2:
		return local.RunModeBubblewrap
	case 3:
		return local.RunModeContainer
	}
	return local.RunModeUnset
}
