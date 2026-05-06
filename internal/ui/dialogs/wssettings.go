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

	runModeRow         *adw.ComboRow
	imageRow           *adw.EntryRow
	binarySourceRow    *adw.ComboRow
	managedEngineRow   *adw.ComboRow
	managedTrackLatest *adw.SwitchRow
	managedVerRow      *adw.EntryRow

	backendID   string
	workspaceID string
	// initial is the on-disk snapshot; persist() compares against it so a
	// no-op edit doesn't cause a needless write.
	initial local.WorkspaceSettings
}

func NewWorkspaceSettings(backendID, workspaceID string) *WorkspaceSettings {
	builder := gtk.NewBuilderFromResource(wsSettingsResource)
	w := &WorkspaceSettings{
		dialog:             uihelpers.MustCast[*adw.PreferencesDialog](builder, "ws_settings_dialog"),
		runModeRow:         uihelpers.MustCast[*adw.ComboRow](builder, "ws_run_mode_row"),
		imageRow:           uihelpers.MustCast[*adw.EntryRow](builder, "ws_image_row"),
		binarySourceRow:    uihelpers.MustCast[*adw.ComboRow](builder, "ws_binary_source_row"),
		managedEngineRow:   uihelpers.MustCast[*adw.ComboRow](builder, "ws_managed_engine_row"),
		managedTrackLatest: uihelpers.MustCast[*adw.SwitchRow](builder, "ws_managed_track_latest_row"),
		managedVerRow:      uihelpers.MustCast[*adw.EntryRow](builder, "ws_managed_version_row"),
		backendID:          backendID,
		workspaceID:        workspaceID,
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

	if current.BinarySource == local.BinarySourceManaged {
		w.binarySourceRow.SetSelected(1)
	} else {
		w.binarySourceRow.SetSelected(0)
	}
	if current.ManagedEngine == "terraform" {
		w.managedEngineRow.SetSelected(1)
	} else {
		w.managedEngineRow.SetSelected(0)
	}
	w.managedTrackLatest.SetActive(current.ManagedTrackLatest)
	w.managedVerRow.SetText(current.ManagedVersion)
	w.updateManagedVisibility()

	w.runModeRow.Connect("notify::selected", w.persist)
	w.imageRow.ConnectApply(w.persist)
	w.binarySourceRow.Connect("notify::selected", func() {
		w.updateManagedVisibility()
		w.persist()
	})
	w.managedEngineRow.Connect("notify::selected", w.persist)
	w.managedTrackLatest.Connect("notify::active", w.persist)
	w.managedVerRow.ConnectApply(w.persist)

	return w
}

func (w *WorkspaceSettings) Present(parent *gtk.Window) {
	w.dialog.Present(parent)
}

func (w *WorkspaceSettings) updateManagedVisibility() {
	managed := w.binarySourceRow.Selected() == 1
	w.managedEngineRow.SetVisible(managed)
	w.managedTrackLatest.SetVisible(managed)
	w.managedVerRow.SetVisible(managed)
}

func (w *WorkspaceSettings) persist() {
	want := local.WorkspaceSettings{
		RunMode:            w.selectedRunMode(),
		Image:              w.imageRow.Text(),
		BinarySource:       w.selectedBinarySource(),
		ManagedEngine:      w.selectedManagedEngine(),
		ManagedTrackLatest: w.managedTrackLatest.Active(),
		ManagedVersion:     w.managedVerRow.Text(),
	}
	if want.BinarySource != local.BinarySourceManaged {
		// Don't carry stale managed values through when the user switched off.
		want.ManagedEngine = ""
		want.ManagedVersion = ""
		want.ManagedTrackLatest = false
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
		"image", want.Image,
		"binary_source", want.BinarySource,
		"managed_engine", want.ManagedEngine,
		"managed_track_latest", want.ManagedTrackLatest,
		"managed_version", want.ManagedVersion)
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

func (w *WorkspaceSettings) selectedBinarySource() local.BinarySource {
	if w.binarySourceRow.Selected() == 1 {
		return local.BinarySourceManaged
	}
	return local.BinarySourceUnset
}

func (w *WorkspaceSettings) selectedManagedEngine() string {
	if w.managedEngineRow.Selected() == 1 {
		return "terraform"
	}
	return "tofu"
}
