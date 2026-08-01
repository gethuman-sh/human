package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// peerCard is an aged, still-running implementation card that handed off a
// branch — the shape a peer daemon's live run takes as seen from another
// machine: no local container, so the liveness probe reports nothing.
func peerCard(now time.Time) []ReconcileCard {
	return []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: autofix/sc-1", now.Add(-StuckRunningGrace-2*time.Minute)),
			cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute)),
		},
	}}
}

func neverReachable(string) ProbeResult { return ProbeResult{Status: ProbeAbsent} }

// The defect: a daemon judged a peer's live run dead from a machine-local
// container probe, reddened the card and retried the stage HERE — against work
// that lives on another disk. The retry then failed on a branch it could not
// check out (SC-1450).
//
// StuckRunningForeignGrace only delays that; reachability forbids it, because
// it is a fact about this disk rather than a judgement about elapsed time.
func TestReconcileStuckRunning_UnreachableBranchIsLeftToItsOwner(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }

	n := reconcileStuckRunning(context.Background(), takeoverSet(peerCard(now), neverReachable),
		liveAgents(), capturingPoster(&posted),
		StageRetry{}, nil, nil, "d1", now, zerolog.Nop())

	require.Zero(t, n, "a card this machine cannot act on must not be reddened")
	assert.Empty(t, posted, "no failed marker may be posted for another machine's work")
}

// The same card, reachable here, is still reddened: the gate must not disable
// hang detection on the machine that actually owns the work.
func TestReconcileStuckRunning_ReachableBranchIsStillReddened(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }

	n := reconcileStuckRunning(context.Background(), takeoverSet(peerCard(now), alwaysReachable),
		liveAgents(), capturingPoster(&posted),
		StageRetry{}, nil, nil, "d1", now, zerolog.Nop())

	require.Equal(t, 1, n)
	require.Len(t, posted, 1)
	assert.Contains(t, posted[0].Body, "Stuck in")
}

// A card with no branch yet — planning, or a build that never handed off — has
// no fact to consult. Refusing there would disable the hang detector for every
// early stage, so those keep the older grace-plus-liveness protection.
func TestReconcileStuckRunning_CardWithoutABranchIsUnaffectedByTheGate(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, neverReachable),
		liveAgents(), capturingPoster(&posted),
		StageRetry{}, nil, nil, "d1", now, zerolog.Nop())

	assert.Equal(t, 1, n, "a branchless card has no reachability fact and keeps the old behaviour")
}

// A nil predicate disables the gate, matching the package's convention for
// optional dependencies — a single-daemon board is unchanged.
func TestReconcileStuckRunning_NilReachableDisablesTheGate(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }

	n := reconcileStuckRunning(context.Background(), takeoverSet(peerCard(now), nil),
		liveAgents(), capturingPoster(&posted),
		StageRetry{}, nil, nil, "d1", now, zerolog.Nop())

	assert.Equal(t, 1, n)
}

// The gate must also stop the RETRY, not merely the marker: relaunching a peer's
// stage here is the half that actually destroys their in-flight run.
func TestReconcileStuckRunning_UnreachableBranchIsNeverRelaunched(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(string, BoardStage) error {
			t.Fatal("a stage whose branch is unreachable here must never be relaunched here")
			return nil
		},
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(peerCard(now), neverReachable),
		liveAgents(), capturingPoster(&posted),
		retry, nil, nil, "d1", now, zerolog.Nop())

	assert.Zero(t, n)
}

// branchActionableHere is the whole gate, so its three inputs are pinned
// directly: no predicate, no branch, and the predicate's own verdict.
func TestBranchActionableHere(t *testing.T) {
	withBranch := BoardCard{Branch: "autofix/sc-1"}

	assert.True(t, branchActionableHere(withBranch, nil), "nil disables the gate")
	assert.True(t, branchActionableHere(BoardCard{}, neverReachable), "no branch, no fact to consult")
	assert.True(t, branchActionableHere(withBranch, alwaysReachable))
	assert.False(t, branchActionableHere(withBranch, neverReachable))
}
