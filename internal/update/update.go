// Package update provides a passive, best-effort version check that runs on
// every CLI invocation. It caches the latest known GitHub release so the
// check adds zero latency on the critical path.
package update

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/afero"
)

const (
	cacheFileName  = "update-check.json"
	checkInterval  = 24 * time.Hour
	cacheMaxAge    = 48 * time.Hour
	githubReleases = "https://api.github.com/repos/StephanSchmidt/human/releases/latest"
)

// fs is the filesystem abstraction — replaced with MemMapFs in tests.
var fs afero.Fs = afero.NewOsFs()

// httpGet is the HTTP client — replaced with a mock in tests.
var httpGet = func(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return http.DefaultClient.Do(req)
}

// updateCache is the persisted JSON for the cached release information.
type updateCache struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

// CachePath returns the path to the update-check cache file.
// Falls back to a relative path when the home directory cannot be resolved.
func CachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", cacheFileName)
	}
	return filepath.Join(home, ".human", cacheFileName)
}

// CheckAndRefresh fetches the latest release from GitHub and writes it to
// cachePath when the existing cache is older than 24 hours. Errors are
// silently discarded — the update check is best-effort.
func CheckAndRefresh(cachePath string) {
	// Skip the network call when the cache is still fresh.
	if isCacheFresh(cachePath, checkInterval) {
		return
	}

	resp, err := httpGet(githubReleases)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil || payload.TagName == "" {
		return
	}

	cache := updateCache{
		LatestVersion: payload.TagName,
		CheckedAt:     time.Now().UTC(),
	}
	writeCache(cachePath, cache)
}

// CachedLatestVersion returns the latest version from the on-disk cache.
// Returns "" when the cache is absent, corrupt, or older than 48 hours.
func CachedLatestVersion(cachePath string) string {
	cache, err := readCache(cachePath)
	if err != nil {
		return ""
	}
	// Discard stale cache entries so a dormant installation does not forever
	// show an outdated "latest" version after the cache has gone cold.
	if time.Since(cache.CheckedAt) > cacheMaxAge {
		return ""
	}
	return cache.LatestVersion
}

// IsNewer reports whether latestVersion is strictly greater than currentVersion
// using semver comparison. Returns false for dev builds, empty strings, or
// inputs that cannot be parsed as valid semver.
func IsNewer(currentVersion, latestVersion string) bool {
	if currentVersion == "" || currentVersion == "dev" {
		return false
	}
	if latestVersion == "" {
		return false
	}
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		return false
	}
	latest, err := semver.NewVersion(latestVersion)
	if err != nil {
		return false
	}
	return latest.GreaterThan(current)
}

// InstallHint returns a human-readable upgrade command appropriate for the
// installation method inferred from the executable path. Falls back to the
// GitHub releases URL when detection is inconclusive.
func InstallHint() string {
	exe, err := os.Executable()
	if err != nil {
		return "https://github.com/StephanSchmidt/human/releases"
	}
	lower := strings.ToLower(exe)

	// Homebrew installs go under Cellar; the standard path is
	// /opt/homebrew/Cellar/... (Apple Silicon) or /usr/local/Cellar/... (Intel).
	if strings.Contains(lower, "cellar") || strings.Contains(lower, "homebrew") {
		return "brew upgrade human"
	}

	// go install places the binary inside $GOPATH/bin or $HOME/go/bin.
	gopath := os.Getenv("GOPATH")
	gobin := os.Getenv("GOBIN")
	homeGoBin := filepath.Join(os.Getenv("HOME"), "go", "bin")
	if (gopath != "" && strings.HasPrefix(exe, filepath.Join(gopath, "bin"))) ||
		(gobin != "" && strings.HasPrefix(exe, gobin)) ||
		strings.HasPrefix(exe, homeGoBin) {
		return "go install github.com/StephanSchmidt/human@latest"
	}

	return "https://github.com/StephanSchmidt/human/releases"
}

// --- internal helpers ---

// isCacheFresh returns true when the cache file exists and was written within
// the given maxAge window.
func isCacheFresh(cachePath string, maxAge time.Duration) bool {
	cache, err := readCache(cachePath)
	if err != nil {
		return false
	}
	return time.Since(cache.CheckedAt) < maxAge
}

func readCache(cachePath string) (updateCache, error) {
	data, err := afero.ReadFile(fs, cachePath)
	if err != nil {
		return updateCache{}, err
	}
	var cache updateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return updateCache{}, err
	}
	return cache, nil
}

func writeCache(cachePath string, cache updateCache) {
	if err := fs.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = afero.WriteFile(fs, cachePath, data, 0o600)
}
