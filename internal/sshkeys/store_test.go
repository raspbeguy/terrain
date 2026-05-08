package sshkeys_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"github.com/raspbeguy/terrain/internal/sshkeys"
)

func setupStore(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

func TestGenerateAndList(t *testing.T) {
	setupStore(t)

	info, err := sshkeys.Generate("github")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if info.Type != "ed25519" {
		t.Errorf("type = %q, want ed25519", info.Type)
	}
	if !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want SHA256: prefix", info.Fingerprint)
	}
	if !strings.HasPrefix(info.Public, "ssh-ed25519 ") {
		t.Errorf("public = %q, want ssh-ed25519 prefix", info.Public)
	}

	keys, err := sshkeys.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || keys[0].Label != "github" {
		t.Fatalf("list = %+v, want one entry labelled github", keys)
	}

	pubText, err := sshkeys.PublicKeyText("github")
	if err != nil {
		t.Fatalf("PublicKeyText: %v", err)
	}
	if pubText != info.Public {
		t.Errorf("PublicKeyText = %q, want %q", pubText, info.Public)
	}

	path, err := sshkeys.PrivateKeyPath("github")
	if err != nil {
		t.Fatalf("PrivateKeyPath: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key perms = %o, want 0600", perm)
	}

	pemBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	if _, err := gossh.ParsePrivateKey(pemBytes); err != nil {
		t.Fatalf("private key not parseable: %v", err)
	}
}

func TestGenerateRejectsDuplicateLabel(t *testing.T) {
	setupStore(t)

	if _, err := sshkeys.Generate("dup"); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := sshkeys.Generate("dup"); !errors.Is(err, sshkeys.ErrLabelTaken) {
		t.Fatalf("second generate err = %v, want ErrLabelTaken", err)
	}
}

func TestGenerateRejectsBadLabel(t *testing.T) {
	setupStore(t)

	for _, bad := range []string{"", "../escape", "with space", "with/slash"} {
		if _, err := sshkeys.Generate(bad); !errors.Is(err, sshkeys.ErrInvalidLabel) {
			t.Errorf("Generate(%q) err = %v, want ErrInvalidLabel", bad, err)
		}
	}
}

func TestImportRoundtrip(t *testing.T) {
	setupStore(t)

	first, err := sshkeys.Generate("source")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	srcPath, _ := sshkeys.PrivateKeyPath("source")
	pemBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}

	imported, err := sshkeys.Import("imported", pemBytes)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.Fingerprint != first.Fingerprint {
		t.Errorf("imported fingerprint = %q, want %q", imported.Fingerprint, first.Fingerprint)
	}
}

func TestRemove(t *testing.T) {
	setupStore(t)

	if _, err := sshkeys.Generate("temp"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := sshkeys.Remove("temp"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := sshkeys.Remove("temp"); !errors.Is(err, sshkeys.ErrNotFound) {
		t.Errorf("second remove err = %v, want ErrNotFound", err)
	}
	keys, _ := sshkeys.List()
	if len(keys) != 0 {
		t.Errorf("list after remove = %+v, want empty", keys)
	}
}

func TestListMissingRoot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	keys, err := sshkeys.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("list = %+v, want empty for fresh data home", keys)
	}
}
