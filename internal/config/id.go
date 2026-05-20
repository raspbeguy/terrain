package config

import (
	"crypto/rand"
	"encoding/hex"
)

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
