package config

import (
	"crypto/rand"
	"encoding/hex"
)

// newID returns a 16-byte hex random identifier suitable for backend/project
// keys. We don't use UUID v4 because we don't need the format guarantees and
// avoiding the dep keeps imports minimal.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is essentially impossible on Linux; falling back
		// to a fixed value would corrupt the registry, so panic.
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
