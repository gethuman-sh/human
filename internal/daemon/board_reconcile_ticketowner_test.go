package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// The ticket-ownership arm (SC-4063): a machine does autonomous work only on
// tickets its own person owns. These pin the arm itself; the end-to-end proof
// that every pass inherits it is below.
func TestWorkGate_TicketOwnership(t *testing.T) {
	gate := WorkGate{reachable: alwaysReachable, identityFor: asPerson("Alice")}
	card := func(assignee, reporter string) ReconcileCard {
		return ReconcileCard{Key: "SC-1", Assignee: assignee, Reporter: reporter}
	}

	t.Run("my ticket is drivable by every intent", func(t *testing.T) {
		c := card("Alice", "Alice")
		assert.Len(t, gate.forReview([]ReconcileCard{c}).cards, 1)
		assert.Len(t, gate.forTakeover([]ReconcileCard{c}).cards, 1)
		assert.Len(t, gate.forOwnWork([]ReconcileCard{c}).cards, 1)
	})

	// The bug this exists for: a peer daemon reddening a review it is not running
	// and cannot observe, on a ticket belonging to someone else.
	t.Run("another person's ticket reaches no intent", func(t *testing.T) {
		c := card("Bob", "Bob")
		assert.Empty(t, gate.forReview([]ReconcileCard{c}).cards)
		assert.Empty(t, gate.forTakeover([]ReconcileCard{c}).cards)
		assert.Empty(t, gate.forOwnWork([]ReconcileCard{c}).cards)
	})

	t.Run("assignee decides when both are recorded", func(t *testing.T) {
		assert.Empty(t, gate.forReview([]ReconcileCard{card("Bob", "Alice")}).cards)
		assert.Len(t, gate.forReview([]ReconcileCard{card("Alice", "Bob")}).cards, 1)
	})

	t.Run("reporter decides when unassigned", func(t *testing.T) {
		assert.Len(t, gate.forReview([]ReconcileCard{card("", "Alice")}).cards, 1)
		assert.Empty(t, gate.forReview([]ReconcileCard{card("", "Bob")}).cards)
	})

	// An unresolved owner is "unknown", never "someone else's". Shortcut hands back
	// an empty name with the error swallowed when its member lookup fails, so
	// reading absence as refusal would let one flaky call stand the pipeline down.
	t.Run("an unresolved owner is worked, not refused", func(t *testing.T) {
		assert.Len(t, gate.forReview([]ReconcileCard{card("", "")}).cards, 1)
	})

	// Upgrade safety: an install that has never declared who it is keeps working
	// exactly as it did before this arm existed.
	t.Run("no declared identity disables the arm", func(t *testing.T) {
		open := WorkGate{reachable: alwaysReachable}
		assert.Len(t, open.forReview([]ReconcileCard{card("Bob", "Bob")}).cards, 1)

		declaredEmpty := WorkGate{reachable: alwaysReachable, identityFor: asPerson()}
		assert.Len(t, declaredEmpty.forReview([]ReconcileCard{card("Bob", "Bob")}).cards, 1)

		unroutable := WorkGate{reachable: alwaysReachable,
			identityFor: func(string) OwnerIdentity { return nil }}
		assert.Len(t, unroutable.forReview([]ReconcileCard{card("Bob", "Bob")}).cards, 1)
	})

	// One person is a display name on one tracker and a login on another, so the
	// identity is a set and the comparison ignores case.
	t.Run("any declared name matches, case-insensitively", func(t *testing.T) {
		both := WorkGate{reachable: alwaysReachable, identityFor: asPerson("Alice", "alice-gh")}
		assert.Len(t, both.forReview([]ReconcileCard{card("ALICE-GH", "")}).cards, 1)
	})
}

// The end-to-end proof of the choke point on the ownership axis: reconcileOnce
// hands every writing pass only its gated set, so another person's card is
// neither reviewed, nor re-driven, nor reddened, nor cleared — by construction,
// through the one gate, with no per-pass check.
func TestReconcileOnce_AnotherPersonsWorkIsUntouchedAcrossEveryPass(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	lister := func(context.Context) ([]ReconcileCard, error) {
		return []ReconcileCard{{
			Key:      "SC-1",
			Assignee: "Bob",
			Reporter: "Bob",
			Comments: []tracker.Comment{
				cmt("[human:ready-for-review]\nbranch: feat/x", now.Add(-StuckRunningGrace-time.Minute)),
				cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-2*time.Minute)),
			},
		}}, nil
	}
	var chained, driven []string
	var posted []struct{ Key, Body string }
	cleared := 0
	reconcileOnce(context.Background(), ReconcileDeps{
		ListCards:    lister,
		Reachable:    alwaysReachable,
		IdentityFor:  asPerson("Alice"),
		MergedProbe:  func(context.Context, string) (bool, error) { return true, nil },
		PostDeployed: func(context.Context, string, string) error { cleared++; return nil },
		LiveAgents:   liveAgents(),
		PostFailed:   capturingPoster(&posted),
		ChainReview:  func(k string) error { chained = append(chained, k); return nil },
		DriveLoop:    func(k string) error { driven = append(driven, k); return nil },
		DaemonID:     "d1",
	})

	assert.Empty(t, chained, "another person's handoff is not reviewed")
	assert.Empty(t, driven, "another person's loop is not re-driven")
	assert.Empty(t, posted, "another person's stuck card is not reddened")
	assert.Zero(t, cleared, "another person's shipped failure is not cleared")
}

// The shipped-failure pass must NOT ride the review gate: a merged PR's branch is
// deleted at merge, so a reachability arm would filter out exactly the cards this
// pass exists to clear. Gating it on ownership alone is what keeps it working.
func TestReconcileShippedFailures_ClearsEvenWhenTheBranchIsGone(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Assignee: "Alice",
		Comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: autofix/sc-1", time.Unix(1, 0)),
			cmt("[human:deploy-failed]\nmerge conflict on main\npr: https://github.com/o/r/pull/7", time.Unix(2, 0)),
		},
	}}
	gate := WorkGate{reachable: neverReachable, identityFor: asPerson("Alice")}
	posted := false
	merged := func(context.Context, string) (bool, error) { return true, nil }
	post := func(context.Context, string, string) error { posted = true; return nil }

	n := reconcileShippedFailures(context.Background(), gate.forOwnWork(cards), ReconcileDeps{MergedProbe: merged, PostDeployed: post})

	assert.Equal(t, 1, n)
	assert.True(t, posted, "an unreachable branch must not hide a shipped PR")
}

// SC-4025. A mid-flight PR review loop is a RUNNING stage, so the re-drive pass
// takes the takeover gate: a loop whose started marker was stamped by another
// daemon is that machine's to finish. The peer cannot judge it — the verdict it
// would read lives in the owning host's state store, so an empty read there says
// nothing about the review, and acting on it reddens a review still in flight.
//
// Driven through reconcileOnce rather than the pass directly, because the defect
// was the pass being handed the wrong gate, not the pass's own logic.
func TestReconcileOnce_PRLoopStartedElsewhereIsNotRedriven(t *testing.T) {
	loopCard := func(machine string) []ReconcileCard {
		return []ReconcileCard{{
			Key:      "SC-1",
			Assignee: "Alice",
			Comments: []tracker.Comment{
				cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
				cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x")+"\nmachine: "+machine, time.Unix(2, 0)),
			},
		}}
	}
	drive := func(cards []ReconcileCard) []string {
		var driven []string
		lister := func(context.Context) ([]ReconcileCard, error) { return cards, nil }
		reconcileOnce(context.Background(), ReconcileDeps{
			ListCards:   lister,
			Reachable:   alwaysReachable,
			IdentityFor: asPerson("Alice"),
			LiveAgents:  liveAgents(),
			ChainReview: func(string) error { return nil },
			DriveLoop:   func(k string) error { driven = append(driven, k); return nil },
			DaemonID:    "d1",
		})
		return driven
	}

	assert.Empty(t, drive(loopCard("d2")), "a peer's running loop is not this machine's to judge")
	// The restart-orphan this pass exists for is unaffected: the daemon id is
	// persisted, so a restarted daemon still matches its own stamp.
	assert.Equal(t, []string{"SC-1"}, drive(loopCard("d1")), "this machine still re-drives its own stalled loop")
}
