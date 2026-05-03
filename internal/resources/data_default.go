//go:build !embed_gresource

package resources

// Data is empty in non-embed builds; Register returns ErrNoResources.
var Data []byte
