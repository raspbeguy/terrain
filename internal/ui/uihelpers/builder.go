package uihelpers

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Panics loudly on builder-ID drift instead of leaking nil widgets.
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
