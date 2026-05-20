package dialogs

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const varEditResource = "/io/github/raspbeguy/Terrain/var-edit.ui"

type VarEditMode int

const (
	VarEditAdd VarEditMode = iota
	VarEditEdit
)

// Sensitive existing variables open with an empty value; we never display the stored secret.
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
