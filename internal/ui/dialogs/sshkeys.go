package dialogs

import (
	"context"
	"log/slog"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/sshkeys"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const sshImportResource = "/io/github/raspbeguy/Terrain/ssh-key-import.ui"

// PresentSSHGenerate prompts for a label, generates a key, and shows the public key for copy-paste.
func PresentSSHGenerate(parent *gtk.Window, onDone func()) {
	d := adw.NewAlertDialog("Generate SSH Key", "Pick a short label — used to refer to the key inside Terrain.")

	entry := gtk.NewEntry()
	entry.SetPlaceholderText("e.g. github")
	entry.SetActivatesDefault(true)
	d.SetExtraChild(entry)

	d.AddResponse("cancel", "Cancel")
	d.AddResponse("create", "Generate")
	d.SetResponseAppearance("create", adw.ResponseSuggested)
	d.SetDefaultResponse("create")
	d.SetCloseResponse("cancel")

	d.ConnectResponse(func(res string) {
		if res != "create" {
			return
		}
		label := strings.TrimSpace(entry.Text())
		info, err := sshkeys.Generate(label)
		if err != nil {
			showAlert(parent, "Couldn't generate key", err.Error())
			return
		}
		if onDone != nil {
			onDone()
		}
		showPublicKey(parent, info)
	})

	d.Present(parent)
}

// PresentSSHImport opens the import dialog (paste-or-pick + label).
func PresentSSHImport(parent *gtk.Window, onDone func()) {
	builder := gtk.NewBuilderFromResource(sshImportResource)

	dialog := uihelpers.MustCast[*adw.Dialog](builder, "ssh_key_import_dialog")
	labelRow := uihelpers.MustCast[*adw.EntryRow](builder, "ssh_key_import_label_row")
	textView := uihelpers.MustCast[*gtk.TextView](builder, "ssh_key_import_textview")
	pickBtn := uihelpers.MustCast[*gtk.Button](builder, "ssh_key_import_pick_button")
	statusRow := uihelpers.MustCast[*adw.ActionRow](builder, "ssh_key_import_status_row")
	confirmBtn := uihelpers.MustCast[*gtk.Button](builder, "ssh_key_import_confirm_button")
	cancelBtn := uihelpers.MustCast[*gtk.Button](builder, "ssh_key_import_cancel_button")

	var pickedBytes []byte

	updateSensitivity := func() {
		buf := textView.Buffer()
		start, end := buf.Bounds()
		hasPaste := strings.TrimSpace(buf.Text(start, end, false)) != ""
		ok := strings.TrimSpace(labelRow.Text()) != "" && (hasPaste || len(pickedBytes) > 0)
		confirmBtn.SetSensitive(ok)
	}
	labelRow.ConnectChanged(updateSensitivity)
	textView.Buffer().ConnectChanged(func() {
		pickedBytes = nil
		updateSensitivity()
	})

	pickBtn.ConnectClicked(func() {
		fd := gtk.NewFileDialog()
		fd.SetTitle("Pick SSH private key")
		fd.SetModal(true)
		fd.Open(context.Background(), parent, func(res gio.AsyncResulter) {
			file, err := fd.OpenFinish(res)
			if err != nil {
				if isFileDialogCancelled(err) {
					return
				}
				statusRow.SetSubtitle("✗ " + err.Error())
				return
			}
			contents, _, err := file.LoadContents(context.Background())
			if err != nil {
				statusRow.SetSubtitle("✗ " + err.Error())
				return
			}
			pickedBytes = contents
			statusRow.SetSubtitle("Loaded " + file.Path())
			updateSensitivity()
		})
	})

	cancelBtn.ConnectClicked(func() { dialog.Close() })

	confirmBtn.ConnectClicked(func() {
		label := strings.TrimSpace(labelRow.Text())
		pem := pickedBytes
		if len(pem) == 0 {
			buf := textView.Buffer()
			start, end := buf.Bounds()
			pem = []byte(buf.Text(start, end, false))
		}
		if len(pem) == 0 {
			statusRow.SetSubtitle("✗ paste a key or pick a file")
			return
		}
		info, err := sshkeys.Import(label, pem)
		if err != nil {
			statusRow.SetSubtitle("✗ " + err.Error())
			return
		}
		slog.Info("ssh key imported", "label", info.Label, "fingerprint", info.Fingerprint)
		dialog.Close()
		if onDone != nil {
			onDone()
		}
	})

	dialog.Present(parent)
}

func showPublicKey(parent *gtk.Window, info sshkeys.KeyInfo) {
	d := adw.NewAlertDialog(info.Label+" — public key",
		"Add this to your forge's SSH-keys settings (GitHub: Settings → SSH and GPG keys → New SSH key).")
	textView := gtk.NewTextView()
	textView.SetMonospace(true)
	textView.SetEditable(false)
	textView.SetWrapMode(gtk.WrapWordChar)
	textView.Buffer().SetText(info.Public)
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(textView)
	scroll.SetMinContentHeight(120)
	scroll.SetHExpand(true)
	d.SetExtraChild(scroll)

	d.AddResponse("close", "Close")
	d.AddResponse("copy", "Copy")
	d.SetResponseAppearance("copy", adw.ResponseSuggested)
	d.SetDefaultResponse("copy")
	d.SetCloseResponse("close")
	d.ConnectResponse(func(res string) {
		if res == "copy" {
			textView.Display().Clipboard().SetText(info.Public)
		}
	})
	d.Present(parent)
}

func isFileDialogCancelled(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == "Dismissed by user" ||
		msg == "g-io-error-quark: Operation was cancelled (19)"
}

func showAlert(parent *gtk.Window, title, body string) {
	d := adw.NewAlertDialog(title, body)
	d.AddResponse("ok", "OK")
	d.SetDefaultResponse("ok")
	d.SetCloseResponse("ok")
	d.Present(parent)
}

