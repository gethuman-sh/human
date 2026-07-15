package daemon

import (
	"bytes"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/gethuman-sh/human/internal/tracker"
)

// descMarkdown and descPolicy render a ticket's markdown description to
// sanitized HTML. GFM matches what trackers actually emit (tables, task
// lists, strikethrough); bluemonday's UGC policy is the allowlist for
// untrusted remote content — descriptions render inside a webview whose
// window.go bindings can drive the daemon, so sanitization must happen here
// on the trusted side, never in the webview itself. Both are safe for
// concurrent use after construction.
var (
	descMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))
	descPolicy   = bluemonday.UGCPolicy()
)

// RenderDescriptionHTML converts a markdown description to sanitized HTML.
// It returns "" for blank input and on conversion errors — the client then
// falls back to showing the raw text, which is always safe to escape.
func RenderDescriptionHTML(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := descMarkdown.Convert([]byte(md), &buf); err != nil {
		return ""
	}
	return descPolicy.Sanitize(buf.String())
}

// NewCachedIssueGetter wraps an issue getter with a stale-while-revalidate
// cache: a key seen before returns instantly from cache while a background
// refresh updates the entry for the next request. The board's detail panel
// re-requests a ticket on every open, so serving the last known copy first
// removes the per-open tracker round-trip from the reading experience without
// the panel ever going more than one open stale. Cache size is bounded by the
// board itself (a fetch caps at 200 issues per project), so no eviction.
func NewCachedIssueGetter(inner func(IssueDetailRequest) (*tracker.Issue, error)) func(IssueDetailRequest) (*tracker.Issue, error) {
	var mu sync.Mutex
	cache := make(map[string]*tracker.Issue)
	inflight := make(map[string]bool)

	cacheKey := func(req IssueDetailRequest) string {
		return req.Kind + "\x00" + req.Tracker + "\x00" + req.Key
	}

	return func(req IssueDetailRequest) (*tracker.Issue, error) {
		key := cacheKey(req)

		mu.Lock()
		if cached, ok := cache[key]; ok {
			// Single-flight: one refresh per key at a time, so a burst of
			// opens doesn't stampede the tracker API.
			if !inflight[key] {
				inflight[key] = true
				go func() {
					fresh, err := inner(req)
					mu.Lock()
					if err == nil {
						// A failed refresh keeps the stale copy: readable
						// beats gone when the tracker blips.
						cache[key] = fresh
					}
					inflight[key] = false
					mu.Unlock()
				}()
			}
			mu.Unlock()
			return cached, nil
		}
		mu.Unlock()

		fresh, err := inner(req)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		cache[key] = fresh
		mu.Unlock()
		return fresh, nil
	}
}
