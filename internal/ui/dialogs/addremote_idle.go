package dialogs

import "github.com/diamondburned/gotk4/pkg/glib/v2"

// Sanctioned IdleAdd outside bridge: one-shot Test Connection callback.
func glibIdleAdd(fn func()) {
	glib.IdleAdd(fn)
}
