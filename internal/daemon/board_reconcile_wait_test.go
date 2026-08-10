package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// heldCard is a card whose answered decision was a sequencing one: the stage it
// names must not start until the ticket the answer defers to is finished.
func heldCard(key string, stage BoardStage, waitsFor string, now time.Time, ago time.Duration) ReconcileCard {
	answered := now.Add(-ago)
	return ReconcileCard{
		Key: key,
		Comments: []tracker.Comment{
			cmt(OptionsHeader+"\nstage: "+string(stage)+"\n1: "+waitsFor+" goes first\nwaits-for-1: "+waitsFor+"\n2: this goes first",
				answered.Add(-time.Minute)),
			cmt(OptionChosenHeader+" 1: "+waitsFor+" goes first\nstage: "+string(stage)+"\nwaits-for: "+waitsFor, answered),
		},
	}
}

func closedProbe(closed bool) ClosedTicketProbe {
	return func(context.Context, string) (bool, error) { return closed, nil }
}

// The pass that starts a decided stage must not start a HELD one — otherwise
// the answer "SC-4245 goes first" is undone two minutes after it is given.
func TestReconcileQueuedLaunch_HoldsWhileTheOtherTicketIsOpen(t *testing.T) {
	now := time.Unix(10_000, 0)
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(),
		takeoverSet([]ReconcileCard{heldCard("SC-1", BoardImplementation, "SC-2", now, time.Hour)}, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), ClosedProbe: closedProbe(false), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"},
		now)

	require.Zero(t, n)
	require.Empty(t, relaunched, "the work waits for the ticket the person put first")
	require.Zero(t, attempts, "a card doing what it was told spends none of the retry budget")
}

// And the other half: the wait is not a dead end. When the ticket it deferred
// to is finished, the stage starts on its own.
func TestReconcileQueuedLaunch_ReleasesWhenTheOtherTicketIsDone(t *testing.T) {
	now := time.Unix(10_000, 0)
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(),
		takeoverSet([]ReconcileCard{heldCard("SC-1", BoardImplementation, "SC-2", now, time.Hour)}, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), ClosedProbe: closedProbe(true), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"},
		now)

	require.Equal(t, 1, n)
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
}

// An unreadable blocker keeps the hold. Starting the work is the one outcome
// the person's answer ruled out, so a tracker blip must never be the thing that
// overrules it — and a deliberate wait has no time bound to give up at.
func TestReconcileQueuedLaunch_UnreadableBlockerKeepsTheHold(t *testing.T) {
	now := time.Unix(10_000, 0)
	for _, probe := range []ClosedTicketProbe{
		nil,
		func(context.Context, string) (bool, error) { return false, errors.WithDetails("tracker unreachable") },
	} {
		var relaunched []BoardStage
		attempts := 0

		n := reconcileQueuedLaunch(context.Background(),
			takeoverSet([]ReconcileCard{heldCard("SC-1", BoardPlanning, "SC-2", now, time.Hour)}, alwaysReachable),
			ReconcileDeps{LiveAgents: liveAgents(), ClosedProbe: probe, Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"},
			now)

		require.Zero(t, n)
		require.Empty(t, relaunched)
	}
}

// A card queued by an ORDINARY answer never consults the probe: it is waiting
// for a launch, not for other work, and holding it would strand it.
func TestReconcileQueuedLaunch_OrdinaryDecisionIsNotHeld(t *testing.T) {
	now := time.Unix(10_000, 0)
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(),
		takeoverSet([]ReconcileCard{decidedCard("SC-1", BoardPlanning, now, time.Hour)}, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), ClosedProbe: closedProbe(false), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"},
		now)

	require.Equal(t, 1, n, "no wait was declared, so nothing holds this card")
	require.Equal(t, []BoardStage{BoardPlanning}, relaunched)
}
