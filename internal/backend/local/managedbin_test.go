package local

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedArchiveURL_Tofu(t *testing.T) {
	archiveURL, sumsURL, name, err := managedArchiveURL("tofu", "1.7.0", "amd64")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if name != "tofu_1.7.0_linux_amd64.zip" {
		t.Errorf("archive name: %s", name)
	}
	if archiveURL != "https://github.com/opentofu/opentofu/releases/download/v1.7.0/tofu_1.7.0_linux_amd64.zip" {
		t.Errorf("archive URL: %s", archiveURL)
	}
	if sumsURL != "https://github.com/opentofu/opentofu/releases/download/v1.7.0/tofu_1.7.0_SHA256SUMS" {
		t.Errorf("sums URL: %s", sumsURL)
	}
}

func TestManagedArchiveURL_Terraform(t *testing.T) {
	archiveURL, sumsURL, name, err := managedArchiveURL("terraform", "1.9.5", "arm64")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if name != "terraform_1.9.5_linux_arm64.zip" {
		t.Errorf("archive name: %s", name)
	}
	if archiveURL != "https://releases.hashicorp.com/terraform/1.9.5/terraform_1.9.5_linux_arm64.zip" {
		t.Errorf("archive URL: %s", archiveURL)
	}
	if sumsURL != "https://releases.hashicorp.com/terraform/1.9.5/terraform_1.9.5_SHA256SUMS" {
		t.Errorf("sums URL: %s", sumsURL)
	}
}

func TestManagedArchiveURL_UnsupportedEngine(t *testing.T) {
	if _, _, _, err := managedArchiveURL("pulumi", "1.0.0", "amd64"); err == nil {
		t.Fatal("expected error for unsupported engine")
	}
}

func TestManagedArchiveURL_UnsupportedArch(t *testing.T) {
	if _, _, _, err := managedArchiveURL("tofu", "1.7.0", "riscv64"); err == nil {
		t.Fatal("expected error for unsupported arch")
	}
}

func TestLookupSHA256(t *testing.T) {
	body := `aaaaaaa  tofu_1.7.0_linux_amd64.zip
bbbbbbb *tofu_1.7.0_darwin_arm64.zip
cccc with spaces in middle
dddddddd  tofu_1.7.0_linux_arm64.zip
`
	if got := lookupSHA256(body, "tofu_1.7.0_linux_amd64.zip"); got != "aaaaaaa" {
		t.Errorf("amd64: got %q", got)
	}
	if got := lookupSHA256(body, "tofu_1.7.0_darwin_arm64.zip"); got != "bbbbbbb" {
		t.Errorf("darwin (with * prefix): got %q", got)
	}
	if got := lookupSHA256(body, "missing.zip"); got != "" {
		t.Errorf("missing: got %q, want empty", got)
	}
}

func TestFileSHA256(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	payload := []byte("hello, terrain")
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := fileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(payload)
	if got != hex.EncodeToString(expected[:]) {
		t.Errorf("hash mismatch: %s", got)
	}
}

func TestExtractZipBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "tofu.zip")
	if err := os.WriteFile(zipPath, buildZip(t, map[string][]byte{
		"tofu":    []byte("fake-binary-payload"),
		"LICENSE": []byte("irrelevant"),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "extracted")
	if err := extractZipBinary(zipPath, "tofu", dst); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-binary-payload" {
		t.Errorf("payload: %q", got)
	}
}

func TestExtractZipBinary_MissingEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "z.zip")
	if err := os.WriteFile(zipPath, buildZip(t, map[string][]byte{
		"LICENSE": []byte("x"),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractZipBinary(zipPath, "tofu", filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected error when binary entry is missing")
	}
}

// Stub-server happy path: archive → SHA256SUMS → install.
func TestManagedResolver_RoundTrip(t *testing.T) {
	if _, err := mapGoArch(runtime.GOARCH); err != nil {
		t.Skipf("unsupported arch %q for this test: %v", runtime.GOARCH, err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	payload := []byte("fake-tofu-binary")
	zipBuf := buildZip(t, map[string][]byte{"tofu": payload})
	zipSum := sha256.Sum256(zipBuf)
	archiveName := fmt.Sprintf("tofu_9.9.9_linux_%s.zip", runtime.GOARCH)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBuf)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(zipSum[:]), archiveName)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := newManagedResolver()
	stagedZip := filepath.Join(t.TempDir(), archiveName)
	if err := r.fetchToFile(context.Background(), srv.URL+"/archive", stagedZip); err != nil {
		t.Fatalf("fetch archive: %v", err)
	}
	sumsBody, err := r.fetchBytes(context.Background(), srv.URL+"/sums")
	if err != nil {
		t.Fatalf("fetch sums: %v", err)
	}
	expected := lookupSHA256(string(sumsBody), archiveName)
	if expected == "" {
		t.Fatalf("expected sum not found in sums body: %s", sumsBody)
	}
	got, err := fileSHA256(stagedZip)
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Errorf("checksum mismatch: %s vs %s", got, expected)
	}
}

func TestManagedResolver_BadEngineOrVersion(t *testing.T) {
	r := newManagedResolver()
	if _, err := r.Resolve(context.Background(), "", "1.0.0"); err == nil {
		t.Error("empty engine should fail")
	}
	if _, err := r.Resolve(context.Background(), "tofu", ""); err == nil {
		t.Error("empty version should fail")
	}
	if _, err := r.Resolve(context.Background(), "kubectl", "1.0.0"); err == nil {
		t.Error("unsupported engine should fail")
	}
}

func TestManagedResolver_UsesCacheWhenPresent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	binPath, err := managedBinaryPath("tofu", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := newManagedResolver()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // confirms we never hit the network when the binary is cached.
	bin, err := r.Resolve(ctx, "tofu", "1.0.0")
	if err != nil {
		t.Fatalf("cached binary should resolve: %v", err)
	}
	if bin.Path != binPath {
		t.Errorf("path: %s", bin.Path)
	}
	if bin.Name != "tofu" {
		t.Errorf("name: %s", bin.Name)
	}
}

func buildZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, payload := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
