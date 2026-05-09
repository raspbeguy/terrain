// Package uihelpers holds tiny GTK utilities shared across UI packages:
// builder casts, CSS loaders, action wiring. Things small enough that giving
// them their own package would be over-engineering, but duplicated everywhere
// would rot.
package uihelpers

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// MustCast fetches a builder object by ID and asserts its concrete gotk4
// type. Builder ID drift between .blp and Go code is a coding error caught at
// startup; panicking surfaces it loudly, instead of leaking nil widgets
// through the UI.
func MustCast[T any](b *gtk.Builder, id string) T {
	obj := b.GetObject(id)
	if obj == nil {
		panic("builder: missing object id " + id)
	}
	v, ok := obj.Cast().(T)
	if !ok {
		var zero T
		panic(fmt.Sprintf("builder: %q is %s, want %T", id, obj.Type().Name(), zero))
	}
	return v
}
