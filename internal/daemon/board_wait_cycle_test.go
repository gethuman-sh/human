package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// heldThread is a ticket whose answered decision defers to waitsFor and whose
// stage has not started since.
func heldThread(waitsFor string, answered time.Time) []tracker.Comment {
	return []tracker.Comment{
		cmt(OptionsHeader+"\nstage: implementation\n1: "+waitsFor+" goes first\nwaits-for-1: "+waitsFor+"\n2: this goes first",
			answered.Add(-time.Minute)),
		cmt(OptionChosenHeader+" 1: "+waitsFor+" goes first\nstage: implementation\nwaits-for: "+waitsFor, answered),
	}
}

// The answer path's half of the guard: SC-4245 is already held on SC-9, so
// "SC-4245 goes first" on SC-9 would leave both waiting for the other with
// nothing able to release either. The click is refused and the block stays
// open (SC-5274).
func TestApplyOption_WaitOnATicketHeldOnThisOneIsRefused(t *testing.T) {
	now := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(sequencingBody, now.Add(-time.Minute)),
		},
		byKey: map[string][]tracker.Comment{"SC-4245": heldThread("SC-9", now.Add(-2*time.Minute))},
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for each other")
	assert.Contains(t, err.Error(), "SC-4245", "the refusal names the pair")
	assert.Empty(t, c.added, "nothing is recorded: the block stays open for a different answer")
	assert.Zero(t, l.calls)
}

// A partner that is held on some third ticket is not a cycle: the wait is
// recorded exactly as before.
func TestApplyOption_WaitOnATicketHeldElsewhereIsRecorded(t *testing.T) {
	now := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(sequencingBody, now.Add(-time.Minute)),
		},
		byKey: map[string][]tracker.Comment{"SC-4245": heldThread("SC-77", now.Add(-2*time.Minute))},
	}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"}))
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], "waits-for: SC-4245")
}

// An unreadable partner does not refuse the click: holding is the direction
// that cannot undo the decision, and the board reports a pair it later finds.
func TestApplyOption_UnreadablePartnerStillRecordsTheWait(t *testing.T) {
	now := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(sequencingBody, now.Add(-time.Minute)),
		},
		listErrFor: "SC-4245",
	}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"}))
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], "waits-for: SC-4245")
}

// The self-wait refusal that predates the cycle guard still holds.
func TestApplyOption_WaitOnItselfIsRefused(t *testing.T) {
	now := time.Now()
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
		cmt("[human:options]\nstage: implementation\n1: SC-9 goes first\nwaits-for-1: SC-9\n2: this goes first", now.Add(-time.Minute)),
	}}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for itself")
	assert.Empty(t, c.added)
}

// The board's half: a pair recorded before the guard is reported on BOTH card
// faces, and a held card whose partner waits for something else is untouched.
func TestMarkWaitCycles_ReportsAPairOnBothCards(t *testing.T) {
	cards := map[string]BoardCard{
		"SC-1": {Stage: BoardImplementation, State: BoardQueued, WaitsFor: "SC-2"},
		"SC-2": {Stage: BoardPlanning, State: BoardQueued, WaitsFor: "SC-1"},
		"SC-3": {Stage: BoardPlanning, State: BoardQueued, WaitsFor: "SC-1"},
		"SC-4": {Stage: BoardPlanning, State: BoardQueued, WaitsFor: "SC-4"},
		"SC-5": {Stage: BoardPlanning, State: BoardRunning},
	}

	MarkWaitCycles(cards)

	assert.Contains(t, cards["SC-1"].Error, "waits for SC-2, which waits for this ticket")
	assert.Contains(t, cards["SC-2"].Error, "waits for SC-1, which waits for this ticket")
	assert.Empty(t, cards["SC-3"].Error, "a one-way wait on a ticket that is itself held is not a cycle")
	assert.Contains(t, cards["SC-4"].Error, "waits for itself")
	assert.Empty(t, cards["SC-5"].Error)
	assert.Equal(t, BoardQueued, cards["SC-1"].State, "the report does not move the card; it is still held")
}

// The release pass's half: the pair is never released — ClosedProbe cannot
// answer yes for either — so it is named in the log, not started and not
// charged, and a person starting one is the way out.
func TestReconcileQueuedLaunch_APairWaitingForEachOtherStaysHeldUncharged(t *testing.T) {
	now := time.Unix(10_000, 0)
	var relaunched []BoardStage
	attempts := 0
	pair := []ReconcileCard{
		heldCard("SC-1", BoardImplementation, "SC-2", now, time.Hour),
		heldCard("SC-2", BoardImplementation, "SC-1", now, time.Hour),
	}
	// A probe answering "closed" for anything would release the pair by
	// accident in the test; the guard must decide before the probe is asked.
	probeAsked := 0
	probe := func(context.Context, string) (bool, error) { probeAsked++; return true, nil }

	n := reconcileQueuedLaunch(context.Background(),
		takeoverSet(pair, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), ClosedProbe: probe, Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"},
		now)

	require.Zero(t, n)
	require.Empty(t, relaunched)
	require.Zero(t, attempts, "a held card spends none of the retry budget, cycle or not")
	require.Zero(t, probeAsked, "the cycle is decided from the cards, before any tracker read")
}
