//go:build embed_gresource

package resources

import _ "embed"

//go:embed terrain.gresource
var Data []byte
