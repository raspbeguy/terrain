// Package dialogs holds modal flows that don't fit inside the main window package.
package dialogs

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type LocalProject struct {
	Path string
	Name string
}

// AddLocal opens a folder picker; cancellation is silent, errors go to onError.
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

// isCancelled string-matches GError messages until gotk4 exposes a typed dismissal.
func isCancelled(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == "Dismissed by user" ||
		msg == "g-io-error-quark: Operation was cancelled (19)" ||
		errors.Is(err, context.Canceled)
}
