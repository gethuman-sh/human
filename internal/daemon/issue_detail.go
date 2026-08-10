package daemon

import (
	"bytes"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
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
//
// The cache is keyed by the request itself. Its three fields are exactly the
// identity of a fetch and all comparable, so Go's own struct-key equality does
// the work a hand-built "kind\x00tracker\x00key" string was doing — with no
// separator to choose, no escaping question, and no way for a field carrying
// the separator to collide with a different request.
func NewCachedIssueGetter(inner func(IssueDetailRequest) (*IssueDetailFetch, error)) func(IssueDetailRequest) (*IssueDetailFetch, error) {
	var mu sync.Mutex
	cache := make(map[IssueDetailRequest]*IssueDetailFetch)
	inflight := make(map[IssueDetailRequest]bool)

	return func(req IssueDetailRequest) (*IssueDetailFetch, error) {
		mu.Lock()
		if cached, ok := cache[req]; ok {
			// Single-flight: one refresh per key at a time, so a burst of
			// opens doesn't stampede the tracker API.
			if !inflight[req] {
				inflight[req] = true
				go func() {
					fresh, err := inner(req)
					mu.Lock()
					if err == nil {
						// A failed refresh keeps the stale copy: readable
						// beats gone when the tracker blips.
						cache[req] = fresh
					}
					inflight[req] = false
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
		cache[req] = fresh
		mu.Unlock()
		return fresh, nil
	}
}
