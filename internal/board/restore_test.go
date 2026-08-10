package board

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// quickView composes what the quick paint composes: open issues with no derived
// BoardCards at all, which is the state RestoreStages exists to repair.
func quickView(keys ...string) daemon.BoardView {
	issues := make([]tracker.Issue, 0, len(keys))
	for _, key := range keys {
		issues = append(issues, tracker.Issue{Key: key, Title: key, Status: "In Progress"})
	}
	return Compose([]daemon.TrackerIssuesResult{pmResult(issues, nil)}, true)
}

// The reported symptom: without a snapshot every in-flight card opens in
// Backlog, because the quick paint derives no stage for any of them.
func TestRestoreStages_QuickPaintWithoutASnapshotIsAllBacklog(t *testing.T) {
	view := quickView("SC-1", "SC-2")

	got := RestoreStages(view, daemon.BoardView{})

	require.Len(t, got.Cards, 2)
	for _, card := range got.Cards {
		assert.Equal(t, string(daemon.BoardBacklog), card.Stage,
			"a card with no derived stage and no snapshot has nowhere else to go")
	}
}

// The fix: a card the last good board had in Verification opens in Verification.
func TestRestoreStages_PutsCardsBackInTheirLastKnownColumn(t *testing.T) {
	view := quickView("SC-1", "SC-2")
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", Stage: string(daemon.BoardVerification)},
		{Key: "SC-2", Stage: string(daemon.BoardImplementation)},
	}}

	got := RestoreStages(view, last)

	assert.Equal(t, string(daemon.BoardVerification), cardByKey(t, got, "SC-1").Stage)
	assert.Equal(t, string(daemon.BoardImplementation), cardByKey(t, got, "SC-2").Stage)
}

// The live fetch owns the ticket SET: a ticket created since the snapshot has no
// last known column and keeps the default rather than disappearing.
func TestRestoreStages_AKeyTheSnapshotNeverSawKeepsItsDefault(t *testing.T) {
	view := quickView("SC-1", "SC-NEW")
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", Stage: string(daemon.BoardVerification)},
	}}

	got := RestoreStages(view, last)

	require.Len(t, got.Cards, 2)
	assert.Equal(t, string(daemon.BoardVerification), cardByKey(t, got, "SC-1").Stage)
	assert.Equal(t, string(daemon.BoardBacklog), cardByKey(t, got, "SC-NEW").Stage)
}

// A ticket that left the board since the snapshot must not come back with it —
// the snapshot answers where a card goes, never whether it exists.
func TestRestoreStages_ASnapshotKeyTheFetchDroppedIsNotResurrected(t *testing.T) {
	view := quickView("SC-1")
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", Stage: string(daemon.BoardVerification)},
		{Key: "SC-CLOSED", Stage: string(daemon.BoardDoneStage)},
	}}

	got := RestoreStages(view, last)

	require.Len(t, got.Cards, 1)
	assert.Equal(t, "SC-1", got.Cards[0].Key)
}

// Only the column is restored: the quick paint marks every card as resolving, so
// a stale badge under that spinner would read as live.
func TestRestoreStages_RestoresTheColumnOnly(t *testing.T) {
	view := quickView("SC-1")
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{{
		Key:     "SC-1",
		Stage:   string(daemon.BoardVerification),
		State:   string(daemon.BoardRunning),
		Verdict: "changes requested",
		Branch:  "fix/sc-1",
	}}}

	got := RestoreStages(view, last)

	card := cardByKey(t, got, "SC-1")
	assert.Equal(t, string(daemon.BoardVerification), card.Stage)
	assert.Empty(t, card.State, "a stale run state must not paint under the resolving spinner")
	assert.Empty(t, card.Verdict)
	assert.Empty(t, card.Branch)
}

// A snapshot card with no stage carries no answer, so it must not blank the
// default the composer already chose.
func TestRestoreStages_ASnapshotCardWithNoStageLeavesTheDefault(t *testing.T) {
	view := quickView("SC-1")
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1"}}}

	got := RestoreStages(view, last)

	assert.Equal(t, string(daemon.BoardBacklog), cardByKey(t, got, "SC-1").Stage)
}

// An empty board is not something a snapshot can populate.
func TestRestoreStages_AnEmptyViewStaysEmpty(t *testing.T) {
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", Stage: string(daemon.BoardVerification)},
	}}

	got := RestoreStages(daemon.BoardView{Error: "daemon unreachable"}, last)

	assert.Empty(t, got.Cards)
	assert.Equal(t, "daemon unreachable", got.Error, "the view's own banner survives the join")
}
