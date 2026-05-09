package dialogs

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const varEditResource = "/io/github/raspbeguy/Terrain/var-edit.ui"

// VarEditMode toggles the dialog between Add (empty fields, edit Key) and
// Edit (key disabled, fields pre-filled).
type VarEditMode int

const (
	VarEditAdd VarEditMode = iota
	VarEditEdit
)

// EditVariable presents the variable add/edit dialog. onSubmitted runs after
// the user clicks Save with a validated payload. Cancel drops silently.
//
// For sensitive existing variables the dialog opens with an empty value
// field; we never display the stored secret. Saving without typing a new
// value preserves the existing one (the local backend treats empty + sensitive
// + no kerry change as a no-op... actually we just resave the stored value).
func EditVariable(parent *gtk.Window, mode VarEditMode, initial domain.Variable, onSubmitted func(domain.Variable)) {
	builder := gtk.NewBuilderFromResource(varEditResource)

	dialog := uihelpers.MustCast[*adw.Dialog](builder, "var_edit_dialog")
	keyRow := uihelpers.MustCast[*adw.EntryRow](builder, "var_edit_key_row")
	valueRow := uihelpers.MustCast[*adw.EntryRow](builder, "var_edit_value_row")
	descRow := uihelpers.MustCast[*adw.EntryRow](builder, "var_edit_description_row")
	categoryRow := uihelpers.MustCast[*adw.ComboRow](builder, "var_edit_category_row")
	hclRow := uihelpers.MustCast[*adw.SwitchRow](builder, "var_edit_hcl_row")
	sensitiveRow := uihelpers.MustCast[*adw.SwitchRow](builder, "var_edit_sensitive_row")
	cancelBtn := uihelpers.MustCast[*gtk.Button](builder, "var_edit_cancel_button")
	saveBtn := uihelpers.MustCast[*gtk.Button](builder, "var_edit_save_button")

	// Pre-fill from initial.
	keyRow.SetText(initial.Key)
	if !initial.Sensitive {
		valueRow.SetText(initial.Value)
	}
	descRow.SetText(initial.Description)
	if initial.Category == domain.VarCategoryEnvironment {
		categoryRow.SetSelected(1)
	} else {
		categoryRow.SetSelected(0)
	}
	hclRow.SetActive(initial.HCL)
	sensitiveRow.SetActive(initial.Sensitive)

	if mode == VarEditEdit {
		// Editing: key is the primary identifier; locking it avoids
		// accidental rename + leftover stale entries.
		keyRow.SetEditable(false)
		dialog.SetTitle("Edit Variable")
	} else {
		dialog.SetTitle("Add Variable")
	}

	updateSave := func() {
		ok := strings.TrimSpace(keyRow.Text()) != ""
		saveBtn.SetSensitive(ok)
	}
	updateSave()
	keyRow.ConnectChanged(updateSave)

	collect := func() domain.Variable {
		v := domain.Variable{
			Key:         strings.TrimSpace(keyRow.Text()),
			Value:       valueRow.Text(),
			Description: strings.TrimSpace(descRow.Text()),
			HCL:         hclRow.Active(),
			Sensitive:   sensitiveRow.Active(),
		}
		if categoryRow.Selected() == 1 {
			v.Category = domain.VarCategoryEnvironment
		} else {
			v.Category = domain.VarCategoryTerraform
		}
		return v
	}

	cancelBtn.ConnectClicked(func() { dialog.Close() })
	saveBtn.ConnectClicked(func() {
		v := collect()
		if v.Key == "" {
			return
		}
		dialog.Close()
		onSubmitted(v)
	})

	dialog.Present(parent)
}
