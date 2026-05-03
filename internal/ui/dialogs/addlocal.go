// Package dialogs holds modal flows that don't fit cleanly inside the main
// window package. M1 only ships the Add Local Project flow; M2/M3/M4 will
// add the New Run dialog, the Variable editor, the Add Remote Backend flow.
package dialogs

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// LocalProject is what AddLocal returns when the user picks a directory.
type LocalProject struct {
	Path string
	Name string
}

// AddLocal opens a folder picker rooted at parent and invokes onAdded with
// the chosen project once the user confirms. Cancellation and errors are
// silently dropped — they're surfaced to the caller via onError if non-nil.
//
// M1 keeps this minimal: just a folder picker. The project is named after
// the directory's basename. M2 will replace this with a richer AdwDialog
// that lets the user override the display name and shows the detected
// tofu/terraform binary.
func AddLocal(
	ctx context.Context,
	parent *gtk.Window,
	onAdded func(LocalProject),
	onError func(error),
) {
	dialog := gtk.NewFileDialog()
	dialog.SetTitle("Add Local Project")
	dialog.SetModal(true)

	dialog.SelectFolder(ctx, parent, func(res gio.AsyncResulter) {
		folder, err := dialog.SelectFolderFinish(res)
		if err != nil {
			// gio.IOErrorEnum DISMISSED maps to user cancellation; treat
			// as no-op rather than an error worth surfacing.
			if isCancelled(err) {
				return
			}
			if onError != nil {
				onError(err)
			}
			return
		}
		path := folder.Path()
		if path == "" {
			if onError != nil {
				onError(errors.New("file dialog returned empty path"))
			}
			return
		}
		project := LocalProject{
			Path: path,
			Name: filepath.Base(path),
		}
		slog.Info("local project picked", "path", project.Path, "name", project.Name)
		onAdded(project)
	})
}

// isCancelled returns true when the FileDialog error indicates the user
// dismissed the dialog (vs. an actual filesystem failure).
func isCancelled(err error) bool {
	if err == nil {
		return false
	}
	// gtk returns a GError with domain GtkDialogError and code 1
	// (DISMISSED) on cancellation; gotk4 wraps it as a generic error.
	// We pattern-match on the message until a typed error is exposed.
	msg := err.Error()
	return msg == "Dismissed by user" ||
		msg == "g-io-error-quark: Operation was cancelled (19)" ||
		errors.Is(err, context.Canceled)
}
