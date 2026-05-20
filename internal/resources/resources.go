// Package resources: gresource bundle; build tag embed_gresource toggles populated vs empty.
package resources

import (
	"errors"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

var ErrNoResources = errors.New("no embedded gresource bundle (built without -tags embed_gresource)")

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
