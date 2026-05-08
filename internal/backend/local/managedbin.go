package local

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type ManagedBinaryInfo struct {
	Engine    string
	Version   string
	Path      string
	SizeBytes int64
}

// managedResolver serializes installs by (engine, version) so renames don't race.
type managedResolver struct {
	httpClient *http.Client

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newManagedResolver() *managedResolver {
	return &managedResolver{
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		locks:      make(map[string]*sync.Mutex),
	}
}

// ErrManagedBinaryMissing tells callers a managed binary isn't installed yet — only the Preferences install dialog should download.
var ErrManagedBinaryMissing = errors.New("managed binary not installed")

// Resolve looks up the binary on disk; never downloads. Run pipeline uses this so launching a run never hits the network.
func (r *managedResolver) Resolve(_ context.Context, engine, version string) (BinaryInfo, error) {
	if engine != "tofu" && engine != "terraform" {
		return BinaryInfo{}, fmt.Errorf("unsupported managed engine %q (want tofu or terraform)", engine)
	}
	if version == "" {
		return BinaryInfo{}, errors.New("managed binary requires a version")
	}
	binPath, err := managedBinaryPath(engine, version)
	if err != nil {
		return BinaryInfo{}, err
	}
	if _, err := os.Stat(binPath); err == nil {
		return BinaryInfo{Name: engine, Path: binPath}, nil
	}
	return BinaryInfo{}, fmt.Errorf("%w: %s %s", ErrManagedBinaryMissing, engine, version)
}

// ProgressFunc reports archive download progress; total may be -1 if Content-Length is missing.
type ProgressFunc func(written, total int64)

// Install downloads + verifies + installs the binary. Caller (Preferences) is responsible for surfacing progress.
func (r *managedResolver) Install(ctx context.Context, engine, version string, progress ProgressFunc) (BinaryInfo, error) {
	if engine != "tofu" && engine != "terraform" {
		return BinaryInfo{}, fmt.Errorf("unsupported managed engine %q (want tofu or terraform)", engine)
	}
	if version == "" {
		return BinaryInfo{}, errors.New("managed binary requires a version")
	}
	binPath, err := managedBinaryPath(engine, version)
	if err != nil {
		return BinaryInfo{}, err
	}
	if _, err := os.Stat(binPath); err == nil {
		return BinaryInfo{Name: engine, Path: binPath}, nil
	}

	lock := r.lockFor(engine + "/" + version)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(binPath); err == nil {
		return BinaryInfo{Name: engine, Path: binPath}, nil
	}
	if err := r.downloadAndInstall(ctx, engine, version, binPath, progress); err != nil {
		return BinaryInfo{}, fmt.Errorf("install %s %s: %w", engine, version, err)
	}
	return BinaryInfo{Name: engine, Path: binPath}, nil
}

func (r *managedResolver) lockFor(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.locks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	r.locks[key] = l
	return l
}

func managedBinaryPath(engine, version string) (string, error) {
	dataHome, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "terrain", "binaries", engine, version, engine), nil
}

func managedArchiveURL(engine, version, goarch string) (archiveURL, sumsURL, archiveName string, err error) {
	arch, err := mapGoArch(goarch)
	if err != nil {
		return "", "", "", err
	}
	switch engine {
	case "tofu":
		archiveName = fmt.Sprintf("tofu_%s_linux_%s.zip", version, arch)
		archiveURL = fmt.Sprintf("https://github.com/opentofu/opentofu/releases/download/v%s/%s", version, archiveName)
		sumsURL = fmt.Sprintf("https://github.com/opentofu/opentofu/releases/download/v%s/tofu_%s_SHA256SUMS", version, version)
	case "terraform":
		archiveName = fmt.Sprintf("terraform_%s_linux_%s.zip", version, arch)
		archiveURL = fmt.Sprintf("https://releases.hashicorp.com/terraform/%s/%s", version, archiveName)
		sumsURL = fmt.Sprintf("https://releases.hashicorp.com/terraform/%s/terraform_%s_SHA256SUMS", version, version)
	default:
		return "", "", "", fmt.Errorf("unsupported engine %q", engine)
	}
	return archiveURL, sumsURL, archiveName, nil
}

func mapGoArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64", "arm", "386":
		return goarch, nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
}

func (r *managedResolver) downloadAndInstall(ctx context.Context, engine, version, binPath string, progress ProgressFunc) error {
	archiveURL, sumsURL, archiveName, err := managedArchiveURL(engine, version, runtime.GOARCH)
	if err != nil {
		return err
	}
	installDir := filepath.Dir(binPath)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", installDir, err)
	}

	// Stage in a sibling temp dir so the final rename is on the same fs.
	tmpDir, err := os.MkdirTemp(filepath.Dir(installDir), engine+"-"+version+"-*.tmp")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, archiveName)
	if err := r.fetchToFile(ctx, archiveURL, archivePath, progress); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}

	sumsBody, err := r.fetchBytes(ctx, sumsURL)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	expected := lookupSHA256(string(sumsBody), archiveName)
	if expected == "" {
		return fmt.Errorf("checksum for %s not found in SHA256SUMS", archiveName)
	}
	actual, err := fileSHA256(archivePath)
	if err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", archiveName, actual, expected)
	}

	staged := filepath.Join(tmpDir, engine)
	if err := extractZipBinary(archivePath, engine, staged); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	return os.Rename(staged, binPath)
}

func (r *managedResolver) fetchToFile(ctx context.Context, url, dst string, progress ProgressFunc) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	src := io.Reader(resp.Body)
	if progress != nil {
		src = &progressReader{r: resp.Body, total: resp.ContentLength, cb: progress}
	}
	_, err = io.Copy(f, src)
	return err
}

// progressReader rate-limits callbacks to ~10/sec so the GTK main thread isn't flooded on a fast network.
type progressReader struct {
	r        io.Reader
	total    int64
	written  int64
	cb       ProgressFunc
	lastFire time.Time
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.written += int64(n)
		now := time.Now()
		if err == io.EOF || now.Sub(p.lastFire) >= 100*time.Millisecond {
			p.cb(p.written, p.total)
			p.lastFire = now
		}
	}
	return n, err
}

func (r *managedResolver) fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// lookupSHA256: filenames may carry a "*" prefix (coreutils binary mode).
func lookupSHA256(sums, filename string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == filename {
			return fields[0]
		}
	}
	return ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func managedBinariesRoot() (string, error) {
	dataHome, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "terrain", "binaries"), nil
}

// ListManagedBinaries: missing cache root → empty list.
func ListManagedBinaries() ([]ManagedBinaryInfo, error) {
	root, err := managedBinariesRoot()
	if err != nil {
		return nil, err
	}
	engines, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []ManagedBinaryInfo
	for _, e := range engines {
		if !e.IsDir() {
			continue
		}
		engine := e.Name()
		if engine != "tofu" && engine != "terraform" {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(root, engine))
		if err != nil {
			continue
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			binPath := filepath.Join(root, engine, v.Name(), engine)
			info, err := os.Stat(binPath)
			if err != nil {
				continue
			}
			out = append(out, ManagedBinaryInfo{
				Engine:    engine,
				Version:   v.Name(),
				Path:      binPath,
				SizeBytes: info.Size(),
			})
		}
	}
	return out, nil
}

// ReferencedManagedBinaries: keys are "<engine>/<version>".
func ReferencedManagedBinaries() (map[string]bool, error) {
	dataHome, err := userDataDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dataHome, "terrain")
	binariesDir := filepath.Join(root, "binaries")

	refs := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path == binariesDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "settings.json" {
			return nil
		}
		s, err := loadWorkspaceSettingsAt(path)
		if err != nil {
			return nil
		}
		if s.BinarySource == BinarySourceManaged && s.ManagedEngine != "" && s.ManagedVersion != "" {
			refs[s.ManagedEngine+"/"+s.ManagedVersion] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func CleanUnusedManagedBinaries() ([]ManagedBinaryInfo, error) {
	refs, err := ReferencedManagedBinaries()
	if err != nil {
		return nil, err
	}
	installed, err := ListManagedBinaries()
	if err != nil {
		return nil, err
	}
	var removed []ManagedBinaryInfo
	for _, b := range installed {
		if refs[b.Engine+"/"+b.Version] {
			continue
		}
		if err := RemoveManagedBinary(b.Engine, b.Version); err != nil {
			return removed, fmt.Errorf("remove %s %s: %w", b.Engine, b.Version, err)
		}
		removed = append(removed, b)
	}
	return removed, nil
}

// RemoveManagedBinary: missing dir is a no-op.
func RemoveManagedBinary(engine, version string) error {
	binPath, err := managedBinaryPath(engine, version)
	if err != nil {
		return err
	}
	versionDir := filepath.Dir(binPath)
	if err := os.RemoveAll(versionDir); err != nil {
		return fmt.Errorf("remove %s: %w", versionDir, err)
	}
	return nil
}

// Singleton so UI installs and runtime resolutions share install locks.
var (
	defaultManagedResolverOnce sync.Once
	defaultManagedResolver     *managedResolver
)

func sharedManagedResolver() *managedResolver {
	defaultManagedResolverOnce.Do(func() {
		defaultManagedResolver = newManagedResolver()
	})
	return defaultManagedResolver
}

// InstallManagedBinaryWithProgress is the Preferences install path; progress fires from the HTTP response reader, rate-limited.
func InstallManagedBinaryWithProgress(ctx context.Context, engine, version string, progress ProgressFunc) (BinaryInfo, error) {
	return sharedManagedResolver().Install(ctx, engine, version, progress)
}

// LatestInstalledVersion returns the highest semver-sorted installed version of engine, or ErrManagedBinaryMissing if none.
func LatestInstalledVersion(engine string) (string, error) {
	bins, err := ListManagedBinaries()
	if err != nil {
		return "", err
	}
	var versions []string
	for _, b := range bins {
		if b.Engine == engine {
			versions = append(versions, b.Version)
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("%w: %s", ErrManagedBinaryMissing, engine)
	}
	sort.Slice(versions, func(i, j int) bool { return compareSemver(versions[i], versions[j]) > 0 })
	return versions[0], nil
}

// compareSemver: -1 if a<b, 0 equal, 1 if a>b. Numeric parse per dotted component; non-numeric falls back to string compare.
func compareSemver(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) || i < len(bp); i++ {
		var as, bs string
		if i < len(ap) {
			as = ap[i]
		}
		if i < len(bp) {
			bs = bp[i]
		}
		ai, aerr := parseUint(as)
		bi, berr := parseUint(bs)
		if aerr == nil && berr == nil {
			if ai < bi {
				return -1
			}
			if ai > bi {
				return 1
			}
			continue
		}
		if as < bs {
			return -1
		}
		if as > bs {
			return 1
		}
	}
	return 0
}

func parseUint(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-numeric")
		}
		v = v*10 + uint64(c-'0')
	}
	return v, nil
}

func extractZipBinary(zipPath, name, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("entry %q not found in %s", name, zipPath)
}
