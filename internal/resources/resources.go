// Package resources embeds the compiled GResource bundle. The bundle byte
// slice (Data) lives in data_embed.go (tag embed_gresource) or data_default.go
// (no tag) so `go build` works without first running meson.
package resources

import (
	"errors"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

var ErrNoResources = errors.New("no embedded gresource bundle (built without -tags embed_gresource)")

// Register: ErrNoResources means the binary was built without the
// embed_gresource tag; soft error during development.
func Register() error {
	if len(Data) == 0 {
		return ErrNoResources
	}
	bytes := glib.NewBytesWithGo(Data)
	res, err := gio.NewResourceFromData(bytes)
	if err != nil {
		return err
	}
	gio.ResourcesRegister(res)
	return nil
}
