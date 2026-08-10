package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// handoffCard is a finished-and-handed-off implementation card naming a branch —
// the orphaned-handoff shape the review chain acts on.
func handoffCard(key, branch string) ReconcileCard {
	return ReconcileCard{
		Key:      key,
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: "+branch, time.Unix(1, 0))},
	}
}

// The gate is the whole point of SC-2047: a work-driving pass may only ever see a
// card this machine can act on. forReview admits a handoff exactly when the
// project is participated in AND the branch resolves here; the pass, given only
// that set, cannot act on anything else.
func TestWorkGate_ForReviewAdmitsOnlyReachableParticipatingCards(t *testing.T) {
	cards := []ReconcileCard{
		handoffCard("SC-here", "reachable/x"),
		handoffCard("SC-elsewhere", "local-only/y"),
	}
	// reachable resolves only the first branch; participation is open.
	reachable := func(b string) ProbeResult {
		if b == "reachable/x" {
			return ProbeResult{Status: ProbePresent}
		}
		return ProbeResult{Status: ProbeAbsent}
	}
	gate := WorkGate{reachable: reachable}

	admitted := gate.forReview(cards)
	assert.Equal(t, []ReconcileCard{cards[0]}, admitted.cards,
		"only the card whose branch resolves here is drivable for review")
}

// A machine that opted a project out never drives it, even when the branch is
// perfectly reachable — participation is a decision that precedes reachability
// (SC-2047 opt-in participation).
func TestWorkGate_ParticipationOptOutRemovesTheCard(t *testing.T) {
	cards := []ReconcileCard{handoffCard("SC-1", "reachable/x")}
	gate := WorkGate{
		reachable:    alwaysReachable,
		participates: func(key string) bool { return false },
	}

	assert.Empty(t, gate.forReview(cards).cards, "an opted-out project yields no drivable cards")
	assert.Empty(t, gate.forTakeover(cards).cards, "opt-out applies to takeover too")
}

// A nil participation predicate means participate everywhere — the backward
// compatible default that keeps single-daemon boards unchanged.
func TestWorkGate_NilParticipationParticipatesEverywhere(t *testing.T) {
	cards := []ReconcileCard{handoffCard("SC-1", "reachable/x")}
	gate := WorkGate{reachable: alwaysReachable, participates: nil}

	assert.Len(t, gate.forReview(cards).cards, 1)
}

// forTakeover binds a still-running stage to its owner by identity: a stage
// stamped by a peer daemon is excluded regardless of branch, so no distant
// machine can red it. This is the pre-branch ownership arm that replaces the
// delay-only foreign grace.
func TestWorkGate_ForTakeoverExcludesForeignOwnedStage(t *testing.T) {
	foreign := ReconcileCard{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt(marker.Sign(ImplementationStartedHeader, "peerAAAA", ""), time.Unix(1, 0))},
	}
	own := ReconcileCard{
		Key:      "SC-2",
		Comments: []tracker.Comment{cmt(marker.Sign(ImplementationStartedHeader, "thisBBBB", ""), time.Unix(1, 0))},
	}
	unstamped := ReconcileCard{
		Key:      "SC-3",
		Comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
	}
	gate := WorkGate{reachable: alwaysReachable, daemonID: "thisBBBB"}

	admitted := gate.forTakeover([]ReconcileCard{foreign, own, unstamped})
	assert.Equal(t, []ReconcileCard{own, unstamped}, admitted.cards,
		"a peer-owned stage is excluded; own and unstamped stages remain takeable")
}

// forReview has NO identity arm: a handoff is the owner's explicit signal that
// implementation finished, so a peer-stamped handoff is still open to any
// participating machine that can reach the branch (ownership ends by finishing).
func TestWorkGate_ForReviewIgnoresStageOwnershipOnceHandedOff(t *testing.T) {
	foreignHandoff := ReconcileCard{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(marker.Sign("[human:ready-for-review]\nbranch: reachable/x", "peerAAAA", ""), time.Unix(1, 0)),
		},
	}
	gate := WorkGate{reachable: alwaysReachable, daemonID: "thisBBBB"}

	assert.Len(t, gate.forReview([]ReconcileCard{foreignHandoff}).cards, 1,
		"a finished-and-handed-off stage is reviewable by any machine that can reach it")
}

// The end-to-end proof of the choke point: reconcileOnce hands each work-driving
// pass only its gated set, so a card whose branch this machine cannot reach is
// neither reviewed nor reddened nor re-driven — by construction, through the one
// gate, with no per-pass check.
func TestReconcileOnce_UnreachableWorkIsUntouchedAcrossEveryPass(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	lister := func(context.Context) ([]ReconcileCard, error) {
		return []ReconcileCard{{
			Key: "SC-1",
			Comments: []tracker.Comment{
				cmt("[human:ready-for-review]\nbranch: local-only/x", now.Add(-StuckRunningGrace-time.Minute)),
				cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-2*time.Minute)),
			},
		}}, nil
	}
	var chained, driven []string
	var posted []struct{ Key, Body string }
	reconcileOnce(context.Background(), ReconcileDeps{
		ListCards:   lister,
		Reachable:   neverReachable,
		LiveAgents:  liveAgents(),
		PostFailed:  capturingPoster(&posted),
		ChainReview: func(k string) error { chained = append(chained, k); return nil },
		DriveLoop:   func(k string) error { driven = append(driven, k); return nil },
		DaemonID:    "d1",
	})

	assert.Empty(t, chained, "an unreachable handoff is not reviewed")
	assert.Empty(t, driven, "an unreachable loop is not re-driven")
	assert.Empty(t, posted, "an unreachable stuck card is not reddened")
}
