package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// An orphaned handoff — a [human:ready-for-review] with no subsequent review
// marker — is exactly the card the live fix→review chain missed on restart, so
// the reconcile pass must launch its review.
func TestReconcileOrphanedHandoffs_LaunchesReviewForOrphan(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
	}}
	var chained []string
	n := reconcileOrphanedHandoffs(cards, func(pmKey string) error {
		chained = append(chained, pmKey)
		return nil
	}, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"SC-1"}, chained)
}

// A review already in flight (review-started) advances the card to the
// verification stage, so the orphan condition (furthest stage ==
// implementation) no longer holds and no second review is launched.
func TestReconcileOrphanedHandoffs_SkipsWhenReviewStarted(t *testing.T) {
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
			cmt("[human:review-started]", time.Unix(2, 0)),
		},
	}}
	called := false
	n := reconcileOrphanedHandoffs(cards, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// A completed review likewise moves the card past implementation, so the
// reconcile pass leaves it alone.
func TestReconcileOrphanedHandoffs_SkipsWhenReviewComplete(t *testing.T) {
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
			cmt("[human:review-complete]\nverdict: pass", time.Unix(2, 0)),
		},
	}}
	called := false
	n := reconcileOrphanedHandoffs(cards, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// A still-building card (implementation-started, no handoff yet) is not an
// orphan — its state is running, not done — so it must never be reviewed.
func TestReconcileOrphanedHandoffs_SkipsRunningBuild(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))},
	}}
	called := false
	n := reconcileOrphanedHandoffs(cards, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// This is the bug: with a fresh watcher (empty HookEventStore, no live exit
// event) nothing rescanned comments on start, so a restart-orphaned handoff
// sat forever. RunBoardReconcile's immediate startup pass must recover it
// without waiting a full interval and without any hook event.
func TestRunBoardReconcile_RecoversOrphanWithNoLiveEvent(t *testing.T) {
	lister := func(context.Context) ([]ReconcileCard, error) {
		return []ReconcileCard{{
			Key:      "SC-1",
			Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
		}}, nil
	}
	chained := make(chan string, 1)
	chain := func(pmKey string) error { chained <- pmKey; return nil }

	ctx := t.Context()
	// A long interval proves the recovery comes from the immediate startup pass,
	// not a ticker tick.
	go RunBoardReconcile(ctx, lister, chain, time.Hour, zerolog.Nop())

	select {
	case pmKey := <-chained:
		assert.Equal(t, "SC-1", pmKey)
	case <-time.After(2 * time.Second):
		t.Fatal("expected the startup reconcile pass to recover the orphaned handoff")
	}
}
