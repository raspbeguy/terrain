package local

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const latestVersionTTL = time.Hour

// HEAD on /releases/latest avoids GitHub API rate limits.
var latestReleaseRepo = map[string]string{
	"tofu":      "opentofu/opentofu",
	"terraform": "hashicorp/terraform",
}

type latestEntry struct {
	version   string
	fetchedAt time.Time
}

var (
	latestCacheMu sync.Mutex
	latestCache   = map[string]latestEntry{}
)

func LatestManagedVersion(ctx context.Context, engine string) (string, error) {
	repo, ok := latestReleaseRepo[engine]
	if !ok {
		return "", fmt.Errorf("unsupported engine %q", engine)
	}

	latestCacheMu.Lock()
	if e, ok := latestCache[engine]; ok && time.Since(e.fetchedAt) < latestVersionTTL {
		latestCacheMu.Unlock()
		return e.version, nil
	}
	latestCacheMu.Unlock()

	v, err := fetchLatestReleaseTag(ctx, repo)
	if err != nil {
		return "", err
	}

	latestCacheMu.Lock()
	latestCache[engine] = latestEntry{version: v, fetchedAt: time.Now()}
	latestCacheMu.Unlock()
	return v, nil
}

func fetchLatestReleaseTag(ctx context.Context, repo string) (string, error) {
	url := "https://github.com/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 3 {
		return "", fmt.Errorf("HEAD %s: %s", url, resp.Status)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no Location header from %s", url)
	}
	parts := strings.Split(loc, "/")
	tag := parts[len(parts)-1]
	if tag == "" || tag == "latest" {
		return "", fmt.Errorf("unexpected redirect target: %s", loc)
	}
	return strings.TrimPrefix(tag, "v"), nil
}
