package tracker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainLister exposes only ListIssues — the common case of a backend that pages
// to completion (or ignores the cap), so its result is never truncated.
type plainLister struct {
	issues []Issue
	err    error
}

func (p plainLister) ListIssues(context.Context, ListOptions) ([]Issue, error) {
	return p.issues, p.err
}

// pagedLister implements the optional PagedLister capability and reports its
// own truncation, standing in for a backend like Linear that enforces the cap.
type pagedLister struct {
	page IssuePage
	err  error
}

func (p pagedLister) ListIssues(context.Context, ListOptions) ([]Issue, error) {
	return p.page.Issues, p.err
}

func (p pagedLister) ListIssuesPage(context.Context, ListOptions) (IssuePage, error) {
	return p.page, p.err
}

func TestListIssuesPage_plainListerNeverTruncated(t *testing.T) {
	l := plainLister{issues: []Issue{{Key: "A-1"}, {Key: "A-2"}}}

	page, err := ListIssuesPage(context.Background(), l, ListOptions{})

	require.NoError(t, err)
	assert.Len(t, page.Issues, 2)
	assert.False(t, page.Truncated, "a plain Lister has no cap signal and must read as complete")
}

func TestListIssuesPage_plainListerPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	l := plainLister{err: boom}

	_, err := ListIssuesPage(context.Background(), l, ListOptions{})

	assert.ErrorIs(t, err, boom)
}

func TestListIssuesPage_prefersPagedCapability(t *testing.T) {
	l := pagedLister{page: IssuePage{Issues: []Issue{{Key: "A-1"}}, Truncated: true}}

	page, err := ListIssuesPage(context.Background(), l, ListOptions{})

	require.NoError(t, err)
	assert.Len(t, page.Issues, 1)
	assert.True(t, page.Truncated, "a PagedLister's truncation signal must be honoured")
}

// issueRange returns n placeholder issues with sequential keys, so a fake page
// fetcher can hand CollectPaged a page of a chosen size.
func issueRange(n int) []Issue {
	issues := make([]Issue, n)
	for i := range issues {
		issues[i] = Issue{Key: fmt.Sprintf("A-%d", i)}
	}
	return issues
}

func TestCollectPaged_noCapFetchesSinglePage(t *testing.T) {
	calls := 0
	page, err := CollectPaged(0, func(int) ([]Issue, bool, error) {
		calls++
		return issueRange(3), true, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, calls, "maxResults<=0 means a single page, never a forced walk")
	assert.Len(t, page.Issues, 3)
	assert.True(t, page.Truncated, "the backend's next-page signal is surfaced even without a cap")
}

func TestCollectPaged_stopsWhenBackendExhausted(t *testing.T) {
	pages := [][]Issue{issueRange(2), issueRange(2)}
	calls := 0
	page, err := CollectPaged(100, func(i int) ([]Issue, bool, error) {
		calls++
		return pages[i], i < len(pages)-1, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Len(t, page.Issues, 4)
	assert.False(t, page.Truncated, "a fetch that ends on the last page is complete, not truncated")
}

func TestCollectPaged_fillsCapAcrossPages(t *testing.T) {
	// per-request page size 100, cap 200: two full pages exactly fill the cap
	// and the second page is the last, so the backlog is complete.
	calls := 0
	page, err := CollectPaged(200, func(i int) ([]Issue, bool, error) {
		calls++
		return issueRange(100), i < 1, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Len(t, page.Issues, 200)
	assert.False(t, page.Truncated)
}

func TestCollectPaged_truncatesWhenMoreRemainAtCap(t *testing.T) {
	// two 100-issue pages fill the cap, and the second page still signals a
	// further page — issues remain beyond the cap.
	page, err := CollectPaged(200, func(int) ([]Issue, bool, error) {
		return issueRange(100), true, nil
	})

	require.NoError(t, err)
	assert.Len(t, page.Issues, 200)
	assert.True(t, page.Truncated)
}

func TestCollectPaged_trimsOvershootAndReportsTruncated(t *testing.T) {
	// a page pushes the total past the cap; the excess is trimmed and the fetch
	// is reported truncated even if that page was the backend's last.
	page, err := CollectPaged(150, func(i int) ([]Issue, bool, error) {
		return issueRange(100), i < 1, nil
	})

	require.NoError(t, err)
	assert.Len(t, page.Issues, 150, "the result is trimmed to the cap")
	assert.True(t, page.Truncated, "50 issues past the cap means truncated")
}

func TestCollectPaged_propagatesFetchError(t *testing.T) {
	boom := errors.New("boom")
	_, err := CollectPaged(100, func(int) ([]Issue, bool, error) {
		return nil, false, boom
	})

	assert.ErrorIs(t, err, boom)
}
