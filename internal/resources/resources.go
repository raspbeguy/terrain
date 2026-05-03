// Package resources embeds the compiled GResource bundle and registers it
// with GLib at application startup.
//
// The actual byte slice lives in one of two sibling files selected by build
// tags:
//   - data_embed.go     (tag: embed_gresource) — populates Data via go:embed
//   - data_default.go   (no tag)               — leaves Data nil, used when
//                                                 building outside of meson
//
// This split lets `go build ./cmd/terrain` succeed on a fresh checkout (no
// meson run, no terrain.gresource file present); the meson custom_target
// invokes `go build -tags embed_gresource` once it has produced the bundle.
package resources

import (
	"errors"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

var ErrNoResources = errors.New("no embedded gresource bundle (built without -tags embed_gresource)")

// Register parses the embedded gresource bundle and registers it globally so
// gtk.Builder.NewBuilderFromResource can resolve /io/github/raspbeguy/Terrain/*
// paths. Returns ErrNoResources when the binary was built without the
// embed_gresource tag — callers should treat that as a soft error during
// development and fall back to a minimal in-code UI.
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
