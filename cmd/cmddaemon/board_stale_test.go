package cmddaemon

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/board"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// goodListing is a listing that actually produced work.
func goodListing() []daemon.TrackerIssuesResult {
	return []daemon.TrackerIssuesResult{{
		TrackerName: "human",
		TrackerRole: "pm",
		Project:     "board",
		Issues:      []tracker.Issue{{Key: "SC-1", Title: "one"}},
	}}
}

// The defect: a refresh that failed handed the board nothing, and an empty
// board is indistinguishable from having no work at all (SC-2005).
func TestStaleListing_FailedRefreshKeepsTheLastGoodCards(t *testing.T) {
	served, err := staleListing(goodListing(), errors.New("resolving 1Password secret via CLI"))

	require.NoError(t, err, "a failed refresh with a remembered listing must not blank the board")
	require.Len(t, served, 2)
	assert.Equal(t, "SC-1", served[0].Issues[0].Key, "the last good cards survive")
}

// Keeping the cards must not hide the failure: a silently stale board that
// looks healthy trades a visible problem for an invisible one.
func TestStaleListing_SaysItIsStale(t *testing.T) {
	served, err := staleListing(goodListing(), errors.New("boom"))

	require.NoError(t, err)
	last := served[len(served)-1]
	assert.Contains(t, last.Err, "this refresh failed")
	assert.Contains(t, last.Err, "boom", "the underlying cause travels with the notice")
	assert.Empty(t, last.Issues, "the staleness notice carries no cards of its own")
}

// With nothing ever fetched there is nothing truer to show than the failure.
func TestStaleListing_NoRememberedListingSurfacesTheError(t *testing.T) {
	served, err := staleListing(nil, errors.New("boom"))

	require.Error(t, err)
	assert.Nil(t, served)
}

// The remembered listing is shared across refreshes, so serving it must not
// let a caller mutate it.
func TestStaleListing_DoesNotMutateTheRememberedListing(t *testing.T) {
	remembered := goodListing()

	served, err := staleListing(remembered, errors.New("boom"))
	require.NoError(t, err)
	served[0].Project = "clobbered"

	assert.Equal(t, "board", remembered[0].Project,
		"the fallback later refreshes depend on must be untouched")
	assert.Len(t, remembered, 1, "the staleness notice must not land in the remembered listing")
}

// Only a listing that produced issues is worth remembering — otherwise one bad
// refresh becomes the "last good" one and pins the board empty for good.
func TestAnyIssues_DistinguishesRealWorkFromAnAllErrorListing(t *testing.T) {
	assert.True(t, anyIssues(goodListing()))

	assert.False(t, anyIssues([]daemon.TrackerIssuesResult{
		{Project: "credentials", Err: "resolving 1Password secret via CLI"},
	}), "an all-error listing must never become the fallback")

	assert.False(t, anyIssues(nil))
}

// The announcement only counts if it reaches the screen: the board dropped it
// because it rides a result that belongs to no tracker, so stale cards rendered
// as current (SC-3554).
func TestStaleListing_StalenessReachesTheBoard(t *testing.T) {
	served, err := staleListing(goodListing(), errors.New("boom"))
	require.NoError(t, err)

	view := board.Compose(served, true)

	assert.Contains(t, view.Error, "this refresh failed", "stale cards must not render as current")
	assert.Contains(t, view.Error, "boom")
	assert.Len(t, view.Cards, 1, "the announcement must not cost the cards")
}
