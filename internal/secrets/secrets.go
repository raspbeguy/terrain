// Package secrets is the single keyring touchpoint (libsecret/Keychain/Credential Vault); other code goes through Get/Set.
package secrets

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const Service = "io.github.raspbeguy.Terrain"

var ErrNotFound = errors.New("secret not found")

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

func Set(key, value string) error {
	if err := keyring.Set(Service, key, value); err != nil {
		return fmt.Errorf("keyring set %q: %w", key, err)
	}
	return nil
}

// Delete: missing keys are not errors.
func Delete(key string) error {
	if err := keyring.Delete(Service, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("keyring delete %q: %w", key, err)
	}
	return nil
}

// Available probes by write-then-delete because a missing key is not the same as a missing service.
func Available() bool {
	const probeKey = ".terrain-availability-probe"
	if err := keyring.Set(Service, probeKey, "1"); err != nil {
		return false
	}
	_ = keyring.Delete(Service, probeKey)
	return true
}

func TokenKey(backendID string) string {
	return "backend/" + backendID + "/token"
}

// GitTokenKey scopes HTTPS git credentials per host so multiple projects share one cred.
func GitTokenKey(host string) string {
	return "git/" + host + "/token"
}

func GitToken(host string) (string, error) {
	return Get(GitTokenKey(host))
}

// SetGitToken stores the HTTPS token for host; empty token deletes the entry.
func SetGitToken(host, token string) error {
	if token == "" {
		return Delete(GitTokenKey(host))
	}
	return Set(GitTokenKey(host), token)
}
