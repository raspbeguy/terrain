// Package secrets stores credential material in the platform's secret store
// (libsecret on Linux via the org.freedesktop.Secret.Service D-Bus interface,
// Keychain on macOS, Credential Vault on Windows). Falls back to plaintext
// in the on-disk config if the secret store is unavailable, with a clear
// warning logged once at startup.
//
// This is the only file that touches the keyring; everything else (config,
// remote backend) goes through Get/Set so we can swap implementations.
package secrets

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// Service is the keyring "service" namespace under which Terrain stores
// secrets. Used as the first argument to keyring.Get/Set.
const Service = "io.github.raspbeguy.Terrain"

// ErrNotFound is returned when the requested secret doesn't exist in the
// store (and no fallback was provided).
var ErrNotFound = errors.New("secret not found")

// Get retrieves a secret by its key (typically backend-id-prefixed). Returns
// ErrNotFound if the key isn't in the store.
func Get(key string) (string, error) {
	v, err := keyring.Get(Service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keyring get %q: %w", key, err)
	}
	return v, nil
}

// Set writes a secret to the store. Overwrites any previous value at key.
func Set(key, value string) error {
	if err := keyring.Set(Service, key, value); err != nil {
		return fmt.Errorf("keyring set %q: %w", key, err)
	}
	return nil
}

// Delete removes a secret. Missing keys are not errors.
func Delete(key string) error {
	if err := keyring.Delete(Service, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("keyring delete %q: %w", key, err)
	}
	return nil
}

// Available reports whether the secret store is reachable. Useful for
// startup probing — if false, callers can warn the user and fall back to
// plaintext storage.
//
// We probe by writing-then-deleting a sentinel value; pure read probes are
// ambiguous (a missing key isn't a missing service).
func Available() bool {
	const probeKey = ".terrain-availability-probe"
	if err := keyring.Set(Service, probeKey, "1"); err != nil {
		return false
	}
	_ = keyring.Delete(Service, probeKey)
	return true
}

// TokenKey returns the conventional storage key for a remote backend's API
// token. Centralised here so the same naming is used everywhere a token is
// looked up.
func TokenKey(backendID string) string {
	return "backend/" + backendID + "/token"
}
