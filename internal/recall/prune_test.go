package recall

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// pagedProvider is a backend that reports whether it cut the listing short —
// the capability Shortcut lacks.
type pagedProvider struct {
	*mockProvider
	truncated bool
}

func (p pagedProvider) ListIssuesPage(_ context.Context, _ tracker.ListOptions) (tracker.IssuePage, error) {
	issues, err := p.listFn(context.Background(), tracker.ListOptions{})
	return tracker.IssuePage{Issues: issues, Truncated: p.truncated}, err
}

// seedEntries indexes n tickets so a later run has something to delete.
func seedEntries(t *testing.T, s *SQLiteStore, n int) {
	t.Helper()
	issues := make([]tracker.Issue, 0, n)
	for i := range n {
		issues = append(issues, tracker.Issue{Key: fmt.Sprintf("KAN-%d", i), Title: "t", Description: "d"})
	}
	_, err := Sync(context.Background(), s, instance(listing(issues...)), true, io.Discard)
	require.NoError(t, err)
}

// syncReturning runs a full sync that lists only these keys, and returns the log.
func syncReturning(t *testing.T, s *SQLiteStore, truncated bool, keys ...string) (*SyncResult, string) {
	t.Helper()
	issues := make([]tracker.Issue, 0, len(keys))
	for _, k := range keys {
		issues = append(issues, tracker.Issue{Key: k, Title: "t", Description: "d"})
	}
	var log strings.Builder
	prov := pagedProvider{mockProvider: listing(issues...), truncated: truncated}
	res, err := Sync(context.Background(), s,
		[]tracker.Instance{{Name: "work", Kind: "jira", Provider: prov}}, true, &log)
	require.NoError(t, err)
	return res, log.String()
}

// THE TEST THAT MATTERS MOST. A listing cut short by the backend looks exactly
// like a backlog that was emptied. Pruning against it deletes history the sync
// merely failed to fetch — silently, and unrecoverably without a re-index.
func TestPrune_TruncatedListingIsRefused(t *testing.T) {
	s := newTestStore(t)
	seedEntries(t, s, 20)

	res, log := syncReturning(t, s, true, "KAN-0")

	assert.Zero(t, res.Pruned, "a truncated listing must delete nothing")
	assert.Contains(t, log, "Skipping prune")
	assert.Contains(t, log, "truncated")
	keys, err := s.AllKeys(context.Background(), "work")
	require.NoError(t, err)
	assert.Len(t, keys, 20, "every entry survives a listing that admits it is short")
}

// The guard cannot depend on the backend admitting truncation: Shortcut does not
// implement PagedLister, so it reports "complete" whether or not the server
// capped the response. A run that would delete most of what we hold is refused
// on the strength of our own data.
func TestPrune_ShortListingIsRefusedEvenWhenBackendClaimsComplete(t *testing.T) {
	s := newTestStore(t)
	seedEntries(t, s, 20)

	res, log := syncReturning(t, s, false, "KAN-0", "KAN-1", "KAN-2")

	assert.Zero(t, res.Pruned, "17 of 20 disappearing at once is not believable")
	assert.Contains(t, log, "Skipping prune")
	assert.Contains(t, log, "17 of 20")
}

// The other half: a genuinely shrunken upstream must still be pruned, or the
// index accumulates tickets that no longer exist.
func TestPrune_GenuineDeletionsStillPrune(t *testing.T) {
	s := newTestStore(t)
	seedEntries(t, s, 20)

	kept := make([]string, 0, 18)
	for i := range 18 {
		kept = append(kept, fmt.Sprintf("KAN-%d", i))
	}
	res, _ := syncReturning(t, s, false, kept...)

	assert.Equal(t, 2, res.Pruned, "a small, believable deletion must still be applied")
	keys, err := s.AllKeys(context.Background(), "work")
	require.NoError(t, err)
	assert.Len(t, keys, 18)
}

// A complete listing that deletes nothing must not log a refusal — the guard
// should be invisible when there is nothing to guard.
func TestPrune_NothingToDeleteIsSilent(t *testing.T) {
	s := newTestStore(t)
	seedEntries(t, s, 3)

	res, log := syncReturning(t, s, false, "KAN-0", "KAN-1", "KAN-2")

	assert.Zero(t, res.Pruned)
	assert.NotContains(t, log, "Skipping prune")
}

// The blast-radius rule itself, pinned directly: a small absolute number of
// deletions is always allowed so ordinary cleanup on a small index is never
// blocked by arithmetic, and a large share is refused however large the index.
func TestRefusePrune(t *testing.T) {
	assert.False(t, refusePrune(0, 100), "nothing to delete is never refused")
	assert.False(t, refusePrune(5, 6), "a small absolute cleanup is allowed even on a tiny index")
	assert.False(t, refusePrune(20, 100), "exactly the permitted share is allowed")
	assert.True(t, refusePrune(21, 100), "beyond the permitted share is refused")
	assert.True(t, refusePrune(90, 100), "a listing that lost almost everything is refused")
	assert.False(t, refusePrune(100, 100000), "a large index tolerates a large absolute cleanup")
}
