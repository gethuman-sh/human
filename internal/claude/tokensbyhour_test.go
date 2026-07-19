package claude

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTokensByHour_buckets(t *testing.T) {
	since := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)

	hour10 := time.Date(2026, 3, 20, 10, 15, 0, 0, time.UTC)
	hour10b := time.Date(2026, 3, 20, 10, 45, 0, 0, time.UTC)
	hour11 := time.Date(2026, 3, 20, 11, 5, 0, 0, time.UTC)
	beforeRange := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

	lines := [][]byte{
		// two assistant lines in the 10:00 hour
		makeLine(t, "assistant", "claude-opus-4-8", hour10, 100, 50, 20, 200),
		makeLine(t, "assistant", "claude-opus-4-8", hour10b, 10, 5, 0, 30),
		// one in the 11:00 hour
		makeLine(t, "assistant", "claude-opus-4-8", hour11, 1, 2, 3, 4),
		// a non-assistant line: excluded
		makeLine(t, "human", "claude-opus-4-8", hour10, 999, 999, 999, 999),
		// out of range: excluded
		makeLine(t, "assistant", "claude-opus-4-8", beforeRange, 999, 999, 999, 999),
	}

	got, err := TokensByHour(fakeWalker{lines: lines}, "/fake", since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("bucket count = %d, want 2 (%+v)", len(got), got)
	}
	// Ascending by bucket key.
	if got[0].Bucket != "2026-03-20 10:00" || got[1].Bucket != "2026-03-20 11:00" {
		t.Fatalf("buckets = %q, %q; want 10:00 then 11:00", got[0].Bucket, got[1].Bucket)
	}
	// Hour 10: fresh = (100+50+20)+(10+5+0) = 185; cacheRead = 200+30 = 230.
	if got[0].Fresh != 185 || got[0].CacheRead != 230 {
		t.Errorf("hour10 fresh/cache = %d/%d, want 185/230", got[0].Fresh, got[0].CacheRead)
	}
	// Hour 11: fresh = 1+2+3 = 6; cacheRead = 4.
	if got[1].Fresh != 6 || got[1].CacheRead != 4 {
		t.Errorf("hour11 fresh/cache = %d/%d, want 6/4", got[1].Fresh, got[1].CacheRead)
	}
}

func TestTokensByHour_empty(t *testing.T) {
	since := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 20, 13, 0, 0, 0, time.UTC)

	lines := [][]byte{
		makeLine(t, "human", "claude-opus-4-8", since, 1, 1, 1, 1),
		[]byte(`{invalid json`),
	}
	got, err := TokensByHour(fakeWalker{lines: lines}, "/fake", since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want no buckets, got %+v", got)
	}
}

func TestClaudeProjectsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ClaudeProjectsRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".claude", "projects")
	if got != want {
		t.Errorf("ClaudeProjectsRoot() = %q, want %q", got, want)
	}
	// Sanity: the returned path is a plain join, not accidentally absolute-rooted
	// elsewhere.
	if !filepath.IsAbs(got) {
		t.Errorf("expected an absolute path, got %q", got)
	}
}
