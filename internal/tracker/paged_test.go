package tracker

import (
	"context"
	"errors"
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
