package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// --- RenderDescriptionHTML tests ---

func TestRenderDescriptionHTML_BasicMarkdown(t *testing.T) {
	html := RenderDescriptionHTML("## Symptom\n\nSome **bold** text\n\n- one\n- two")
	assert.Contains(t, html, "<h2>Symptom</h2>")
	assert.Contains(t, html, "<strong>bold</strong>")
	assert.Contains(t, html, "<li>one</li>")
}

func TestRenderDescriptionHTML_GFMTable(t *testing.T) {
	html := RenderDescriptionHTML("| a | b |\n|---|---|\n| 1 | 2 |")
	assert.Contains(t, html, "<table>")
	assert.Contains(t, html, "<td>1</td>")
}

// Descriptions are untrusted remote content rendered in a webview whose
// bindings can drive the daemon — script/handler survival here would be an
// XSS straight into board control, so sanitization is the load-bearing part.
func TestRenderDescriptionHTML_SanitizesUntrustedHTML(t *testing.T) {
	html := RenderDescriptionHTML("hello <script>alert(1)</script> **world**\n\n<img src=x onerror=alert(1)>")
	assert.NotContains(t, html, "<script")
	assert.NotContains(t, html, "onerror")
	assert.Contains(t, html, "<strong>world</strong>")
}

func TestRenderDescriptionHTML_Empty(t *testing.T) {
	assert.Equal(t, "", RenderDescriptionHTML(""))
	assert.Equal(t, "", RenderDescriptionHTML("  \n\t"))
}

// --- NewCachedIssueGetter tests ---

func TestCachedIssueGetter_MissFetchesOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	getter := NewCachedIssueGetter(func(req IssueDetailRequest) (*IssueDetailFetch, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &IssueDetailFetch{Issue: tracker.Issue{Key: req.Key, Description: "v1"}}, nil
	})

	issue, err := getter(IssueDetailRequest{Kind: "shortcut", Tracker: "human", Key: "1"})
	require.NoError(t, err)
	assert.Equal(t, "v1", issue.Issue.Description)
	mu.Lock()
	assert.Equal(t, 1, calls)
	mu.Unlock()
}

func TestCachedIssueGetter_HitServesStaleThenRevalidates(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	getter := NewCachedIssueGetter(func(req IssueDetailRequest) (*IssueDetailFetch, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		return &IssueDetailFetch{Issue: tracker.Issue{Key: req.Key, Description: fmt.Sprintf("v%d", n)}}, nil
	})
	req := IssueDetailRequest{Kind: "shortcut", Tracker: "human", Key: "1"}

	first, err := getter(req)
	require.NoError(t, err)
	assert.Equal(t, "v1", first.Issue.Description)

	// Second call must serve the cached copy instantly (stale is fine) while
	// a background refresh updates the entry for a later call.
	second, err := getter(req)
	require.NoError(t, err)
	assert.Equal(t, "v1", second.Issue.Description)

	assert.Eventually(t, func() bool {
		issue, gErr := getter(req)
		return gErr == nil && issue.Issue.Description != "v1"
	}, 2*time.Second, 10*time.Millisecond, "background refresh never landed")
}

func TestCachedIssueGetter_ErrorIsNotCached(t *testing.T) {
	var mu sync.Mutex
	fail := true
	getter := NewCachedIssueGetter(func(req IssueDetailRequest) (*IssueDetailFetch, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.WithDetails("tracker down")
		}
		return &IssueDetailFetch{Issue: tracker.Issue{Key: req.Key, Description: "ok"}}, nil
	})
	req := IssueDetailRequest{Kind: "shortcut", Tracker: "human", Key: "1"}

	_, err := getter(req)
	require.Error(t, err)

	mu.Lock()
	fail = false
	mu.Unlock()
	issue, err := getter(req)
	require.NoError(t, err)
	assert.Equal(t, "ok", issue.Issue.Description)
}

func TestCachedIssueGetter_FailedRefreshKeepsStaleCopy(t *testing.T) {
	var mu sync.Mutex
	fail := false
	getter := NewCachedIssueGetter(func(req IssueDetailRequest) (*IssueDetailFetch, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.WithDetails("tracker blip")
		}
		return &IssueDetailFetch{Issue: tracker.Issue{Key: req.Key, Description: "v1"}}, nil
	})
	req := IssueDetailRequest{Kind: "shortcut", Tracker: "human", Key: "1"}

	_, err := getter(req)
	require.NoError(t, err)

	mu.Lock()
	fail = true
	mu.Unlock()

	// Every subsequent call keeps returning the stale copy: readable beats
	// gone while the tracker misbehaves.
	for range 3 {
		issue, gErr := getter(req)
		require.NoError(t, gErr)
		assert.Equal(t, "v1", issue.Issue.Description)
		time.Sleep(20 * time.Millisecond)
	}
}
