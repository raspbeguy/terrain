package dialogs

import (
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/local"
)

func PresentNewWorkspace(parent *gtk.Window, projectName string, onConfirmed func(name string)) {
	d := adw.NewAlertDialog(
		"New Workspace",
		"Pick a name for the new Terraform workspace under "+projectName+`. Allowed: letters, digits, hyphen, underscore.`)

	entry := gtk.NewEntry()
	entry.SetPlaceholderText("e.g. staging")
	entry.SetActivatesDefault(true)
	d.SetExtraChild(entry)

	d.AddResponse("cancel", "Cancel")
	d.AddResponse("create", "Create")
	d.SetResponseAppearance("create", adw.ResponseSuggested)
	d.SetDefaultResponse("create")
	d.SetCloseResponse("cancel")
	d.SetResponseEnabled("create", false)

	entry.ConnectChanged(func() {
		d.SetResponseEnabled("create", local.IsValidWorkspaceName(strings.TrimSpace(entry.Text())))
	})

	d.ConnectResponse(func(res string) {
		if res != "create" {
			return
		}
		name := strings.TrimSpace(entry.Text())
		if !local.IsValidWorkspaceName(name) {
			return
		}
		onConfirmed(name)
	})

	d.Present(parent)
}
