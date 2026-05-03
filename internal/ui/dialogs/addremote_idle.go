package dialogs

import "github.com/diamondburned/gotk4/pkg/glib/v2"

// glibIdleAdd is the minimal main-thread marshaller used by AddRemote's
// async Test Connection path. The bridge package is for streaming domain
// events; one-shot UI updates from an isolated callback live here to keep
// the bridge surface tight.
func glibIdleAdd(fn func()) {
	glib.IdleAdd(fn)
}
