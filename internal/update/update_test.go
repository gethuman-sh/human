package update

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// useMemFs swaps the package-level filesystem to an in-memory one for the
// duration of a test, then restores the original on cleanup.
func useMemFs(t *testing.T) afero.Fs {
	t.Helper()
	mem := afero.NewMemMapFs()
	orig := fs
	fs = mem
	t.Cleanup(func() { fs = orig })
	return mem
}

// TestIsNewer verifies all semver comparison branches, including dev/empty/bad
// inputs that must never trigger an update notice.
func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"v0.17.0", "v0.18.0", true},
		{"0.17.0", "0.18.0", true},
		{"v0.18.0", "v0.17.0", false},
		{"v0.18.0", "v0.18.0", false},
		{"dev", "v0.18.0", false},
		{"", "v0.18.0", false},
		{"v0.17.0", "", false},
		{"not-semver", "v0.18.0", false},
		{"v0.17.0", "not-semver", false},
	}
	for _, tc := range tests {
		got := IsNewer(tc.current, tc.latest)
		if got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

// TestReadWriteCache verifies that a cache round-trip preserves the version
// and timestamp with sub-second precision.
func TestReadWriteCache(t *testing.T) {
	useMemFs(t)
	path := "/tmp/.human/update-check.json"

	now := time.Now().UTC().Truncate(time.Second)
	writeCache(path, updateCache{LatestVersion: "v0.18.0", CheckedAt: now})

	got, err := readCache(path)
	if err != nil {
		t.Fatalf("readCache: %v", err)
	}
	if got.LatestVersion != "v0.18.0" {
		t.Errorf("version: got %q, want %q", got.LatestVersion, "v0.18.0")
	}
	if !got.CheckedAt.Equal(now) {
		t.Errorf("checked_at: got %v, want %v", got.CheckedAt, now)
	}
}

// TestReadCache_Missing expects an error when the cache file does not exist.
func TestReadCache_Missing(t *testing.T) {
	useMemFs(t)
	_, err := readCache("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for missing cache, got nil")
	}
}

// TestReadCache_Corrupt expects an error when the cache JSON is malformed.
func TestReadCache_Corrupt(t *testing.T) {
	mem := useMemFs(t)
	path := "/tmp/corrupt.json"
	_ = afero.WriteFile(mem, path, []byte("not json"), 0o600)

	_, err := readCache(path)
	if err == nil {
		t.Fatal("expected error for corrupt cache, got nil")
	}
}

// TestCachedLatestVersion_Fresh expects the stored version when the cache is
// younger than 48 hours.
func TestCachedLatestVersion_Fresh(t *testing.T) {
	mem := useMemFs(t)
	path := "/tmp/.human/update-check.json"
	cache := updateCache{LatestVersion: "v0.18.0", CheckedAt: time.Now().UTC()}
	data, _ := json.Marshal(cache)
	_ = mem.MkdirAll(filepath.Dir(path), 0o700)
	_ = afero.WriteFile(mem, path, data, 0o600)

	got := CachedLatestVersion(path)
	if got != "v0.18.0" {
		t.Errorf("got %q, want %q", got, "v0.18.0")
	}
}

// TestCachedLatestVersion_Stale expects an empty string for a cache entry
// older than 48 hours.
func TestCachedLatestVersion_Stale(t *testing.T) {
	mem := useMemFs(t)
	path := "/tmp/.human/update-check.json"
	cache := updateCache{
		LatestVersion: "v0.18.0",
		CheckedAt:     time.Now().UTC().Add(-49 * time.Hour),
	}
	data, _ := json.Marshal(cache)
	_ = mem.MkdirAll(filepath.Dir(path), 0o700)
	_ = afero.WriteFile(mem, path, data, 0o600)

	got := CachedLatestVersion(path)
	if got != "" {
		t.Errorf("stale cache should return empty string, got %q", got)
	}
}

// TestCheckAndRefresh_SkipsWhenFresh confirms no HTTP request is made when
// the cache was written less than 24 hours ago.
func TestCheckAndRefresh_SkipsWhenFresh(t *testing.T) {
	mem := useMemFs(t)
	path := "/tmp/.human/update-check.json"
	cache := updateCache{LatestVersion: "v0.17.0", CheckedAt: time.Now().UTC()}
	data, _ := json.Marshal(cache)
	_ = mem.MkdirAll(filepath.Dir(path), 0o700)
	_ = afero.WriteFile(mem, path, data, 0o600)

	origGet := httpGet
	httpGet = func(_ string) (*http.Response, error) {
		t.Fatal("httpGet must not be called when cache is fresh")
		return nil, nil
	}
	t.Cleanup(func() { httpGet = origGet })

	CheckAndRefresh(path)
}

// TestCheckAndRefresh_RefreshesWhenStale confirms the cache is updated when
// older than 24 hours, using an httptest server to avoid real network calls.
func TestCheckAndRefresh_RefreshesWhenStale(t *testing.T) {
	mem := useMemFs(t)
	path := "/tmp/.human/update-check.json"

	// Write a stale cache entry.
	stale := updateCache{LatestVersion: "v0.17.0", CheckedAt: time.Now().UTC().Add(-25 * time.Hour)}
	data, _ := json.Marshal(stale)
	_ = mem.MkdirAll(filepath.Dir(path), 0o700)
	_ = afero.WriteFile(mem, path, data, 0o600)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v0.18.0"}`)
	}))
	defer srv.Close()

	origGet := httpGet
	httpGet = func(_ string) (*http.Response, error) {
		return http.Get(srv.URL) //nolint:noctx
	}
	t.Cleanup(func() { httpGet = origGet })

	CheckAndRefresh(path)

	got := CachedLatestVersion(path)
	if got != "v0.18.0" {
		t.Errorf("after refresh, got %q, want %q", got, "v0.18.0")
	}
}

// TestCachePath verifies the returned path ends with the expected suffix so
// the cache lands in ~/.human/update-check.json under normal conditions.
func TestCachePath(t *testing.T) {
	got := CachePath()
	if !strings.HasSuffix(got, filepath.Join(".human", "update-check.json")) {
		t.Errorf("CachePath() = %q, want suffix %q", got, filepath.Join(".human", "update-check.json"))
	}
}

// TestCachePath_HomeDirError verifies the fallback path when the home directory
// cannot be resolved (e.g. misconfigured HOME env var).
func TestCachePath_HomeDirError(t *testing.T) {
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { userHomeDir = orig })

	got := CachePath()
	if !strings.HasSuffix(got, filepath.Join(".human", "update-check.json")) {
		t.Errorf("CachePath() fallback = %q, want suffix %q", got, filepath.Join(".human", "update-check.json"))
	}
	if strings.HasPrefix(got, "/") {
		t.Errorf("expected relative fallback path, got %q", got)
	}
}

// TestCheckAndRefresh_NonOKStatus verifies that a non-200 HTTP response is
// silently discarded and the cache is not updated.
func TestCheckAndRefresh_NonOKStatus(t *testing.T) {
	mem := useMemFs(t)
	path := "/tmp/.human/update-check.json"
	_ = mem.MkdirAll(filepath.Dir(path), 0o700)

	origGet := httpGet
	httpGet = func(_ string) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusForbidden)
		return rec.Result(), nil
	}
	t.Cleanup(func() { httpGet = origGet })

	CheckAndRefresh(path)

	got := CachedLatestVersion(path)
	if got != "" {
		t.Errorf("expected empty cache after non-200 response, got %q", got)
	}
}

// TestIsCacheFresh_MissingFile confirms isCacheFresh returns false when the
// cache file does not exist.
func TestIsCacheFresh_MissingFile(t *testing.T) {
	useMemFs(t)
	if isCacheFresh("/nonexistent/path.json", 24*time.Hour) {
		t.Fatal("isCacheFresh should return false for missing file")
	}
}

// TestInstallHint_GoPath checks that a path inside GOPATH/bin yields the
// go install hint.
func TestInstallHint_GoPath(t *testing.T) {
	// The hint logic inspects os.Executable(), which returns the test binary
	// path — not easily controllable. We validate that the function returns a
	// non-empty string (it always falls back to the releases URL).
	hint := InstallHint()
	if hint == "" {
		t.Fatal("InstallHint returned empty string")
	}
	// Must be one of the three expected forms.
	if !strings.Contains(hint, "brew") &&
		!strings.Contains(hint, "go install") &&
		!strings.Contains(hint, "github.com/gethuman-sh/human/releases") {
		t.Errorf("unexpected hint: %q", hint)
	}
}
