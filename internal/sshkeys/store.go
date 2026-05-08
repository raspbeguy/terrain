// Package sshkeys stores keys under $XDG_DATA_HOME so the Flatpak sandbox needs no ssh-auth or ~/.ssh access.
package sshkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

type KeyInfo struct {
	Label       string    `json:"-"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at"`
	Fingerprint string    `json:"fingerprint"`
	Public      string    `json:"-"`
}

var ErrNotFound = errors.New("ssh key not found")
var ErrLabelTaken = errors.New("ssh key label already exists")
var ErrInvalidLabel = errors.New("invalid ssh key label")

var labelRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Generate writes an unencrypted ed25519 keypair under label; encrypt-at-rest is out of scope.
func Generate(label string) (KeyInfo, error) {
	if !labelRE.MatchString(label) {
		return KeyInfo{}, ErrInvalidLabel
	}
	dir, err := keyDir(label)
	if err != nil {
		return KeyInfo{}, err
	}
	if _, err := os.Stat(dir); err == nil {
		return KeyInfo{}, ErrLabelTaken
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("generate ed25519: %w", err)
	}
	privBlock, err := gossh.MarshalPrivateKey(priv, "terrain-managed key "+label)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("marshal private key: %w", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("ssh public key: %w", err)
	}
	pubLine := strings.TrimRight(string(gossh.MarshalAuthorizedKey(sshPub)), "\n") +
		" terrain:" + label

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return KeyInfo{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := writeFile(filepath.Join(dir, "id_ed25519"),
		pem.EncodeToMemory(privBlock), 0o600); err != nil {
		return KeyInfo{}, err
	}
	if err := writeFile(filepath.Join(dir, "id_ed25519.pub"),
		[]byte(pubLine+"\n"), 0o644); err != nil {
		return KeyInfo{}, err
	}
	info := KeyInfo{
		Label:       label,
		Type:        "ed25519",
		CreatedAt:   time.Now().UTC(),
		Fingerprint: gossh.FingerprintSHA256(sshPub),
		Public:      pubLine,
	}
	if err := writeMetadata(dir, info); err != nil {
		return KeyInfo{}, err
	}
	return info, nil
}

// Import accepts any unencrypted OpenSSH/RSA/EC/ED25519 private key; passphrases aren't supported.
func Import(label string, pem []byte) (KeyInfo, error) {
	if !labelRE.MatchString(label) {
		return KeyInfo{}, ErrInvalidLabel
	}
	dir, err := keyDir(label)
	if err != nil {
		return KeyInfo{}, err
	}
	if _, err := os.Stat(dir); err == nil {
		return KeyInfo{}, ErrLabelTaken
	}

	signer, err := gossh.ParsePrivateKey(pem)
	if err != nil {
		if _, isMissing := err.(*gossh.PassphraseMissingError); isMissing {
			return KeyInfo{}, fmt.Errorf("passphrase-protected keys are not supported")
		}
		return KeyInfo{}, fmt.Errorf("parse private key: %w", err)
	}
	sshPub := signer.PublicKey()
	pubLine := strings.TrimRight(string(gossh.MarshalAuthorizedKey(sshPub)), "\n") +
		" terrain:" + label

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return KeyInfo{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := writeFile(filepath.Join(dir, "id_ed25519"), normalizeNewlines(pem), 0o600); err != nil {
		return KeyInfo{}, err
	}
	if err := writeFile(filepath.Join(dir, "id_ed25519.pub"),
		[]byte(pubLine+"\n"), 0o644); err != nil {
		return KeyInfo{}, err
	}
	info := KeyInfo{
		Label:       label,
		Type:        sshPub.Type(),
		CreatedAt:   time.Now().UTC(),
		Fingerprint: gossh.FingerprintSHA256(sshPub),
		Public:      pubLine,
	}
	if err := writeMetadata(dir, info); err != nil {
		return KeyInfo{}, err
	}
	return info, nil
}

// List returns all stored keys, sorted by label. Missing root → empty slice.
func List() ([]KeyInfo, error) {
	root, err := keysRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]KeyInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// Get loads a single key by label.
func Get(label string) (KeyInfo, error) {
	if !labelRE.MatchString(label) {
		return KeyInfo{}, ErrInvalidLabel
	}
	return load(label)
}

// Remove wipes the key dir; missing label is ErrNotFound.
func Remove(label string) error {
	if !labelRE.MatchString(label) {
		return ErrInvalidLabel
	}
	dir, err := keyDir(label)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return os.RemoveAll(dir)
}

// PublicKeyText returns the authorized_keys-format string for label.
func PublicKeyText(label string) (string, error) {
	info, err := Get(label)
	if err != nil {
		return "", err
	}
	return info.Public, nil
}

// PrivateKeyPath returns the on-disk path of label's private key.
func PrivateKeyPath(label string) (string, error) {
	dir, err := keyDir(label)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	return filepath.Join(dir, "id_ed25519"), nil
}

func load(label string) (KeyInfo, error) {
	dir, err := keyDir(label)
	if err != nil {
		return KeyInfo{}, err
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return KeyInfo{}, ErrNotFound
		}
		return KeyInfo{}, err
	}
	var info KeyInfo
	if err := json.Unmarshal(metaBytes, &info); err != nil {
		return KeyInfo{}, fmt.Errorf("parse metadata for %s: %w", label, err)
	}
	info.Label = label

	pubBytes, err := os.ReadFile(filepath.Join(dir, "id_ed25519.pub"))
	if err == nil {
		info.Public = strings.TrimRight(string(pubBytes), "\n")
	}
	if info.Fingerprint == "" && info.Public != "" {
		if pk, _, _, _, err := gossh.ParseAuthorizedKey(pubBytes); err == nil {
			info.Fingerprint = gossh.FingerprintSHA256(pk)
		}
	}
	return info, nil
}

func writeMetadata(dir string, info KeyInfo) error {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	return writeFile(filepath.Join(dir, "metadata.json"), append(b, '\n'), 0o600)
}

func writeFile(path string, data []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func keyDir(label string) (string, error) {
	root, err := keysRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, label), nil
}

func keysRoot() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "terrain", "ssh-keys"), nil
}

func normalizeNewlines(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
}
