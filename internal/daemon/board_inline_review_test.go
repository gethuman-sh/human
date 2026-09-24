package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/tracker"
)

// SC-5476: one handoff, two reviewers. A board fix run posts its handoff and
// then, seconds later, its own [human:review-started] — and in that gap the
// daemon's launchers see finished work with nobody reviewing it and start a
// second reviewer that no claim can arbitrate, because the in-container
// reviewer posts none. The handoff must carry the fact.

const sc5476Handoff = "[human:ready-for-review]\nbranch: autofix/sc-5396\ncommits: e5409b74, 56145a07\nreview: inline"

// sc5476ThreadAt57 is the SC-5396 thread as the daemon read it at 10:00:57 —
// the handoff is one second old and the container's own review-started has not
// been posted yet.
func sc5476ThreadAt57(base time.Time) []tracker.Comment {
	return []tracker.Comment{
		cmt(ImplementationStartedHeader, base.Add(-40*time.Minute)),
		cmt(sc5476Handoff, base.Add(56*time.Second)),
	}
}

func TestReconcileOrphanedHandoffs_InlineHandoffLiveContainer_NoSecondReview(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	cards := []ReconcileCard{{Key: "SC-5396", Comments: sc5476ThreadAt57(base)}}
	chained := 0
	n := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		LiveAgents:  liveAgents("board-SC-5396-implementation"),
		ChainReview: func(string) error { chained++; return nil },
	}, base.Add(57*time.Second))
	assert.Equal(t, 0, n)
	assert.Zero(t, chained, "the handoff says its poster is reviewing it and that container is alive")
}

func TestReconcileOrphanedHandoffs_InlineHandoffDeadContainer_StillChains(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	cards := []ReconcileCard{{Key: "SC-5396", Comments: sc5476ThreadAt57(base)}}
	chained := 0
	n := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		LiveAgents:  liveAgents(), // empty set: liveness is KNOWN and the agent is gone
		ChainReview: func(string) error { chained++; return nil },
	}, base.Add(58*time.Second))
	assert.Equal(t, 1, n, "SC-430 orphan recovery must survive, and survive immediately rather than after the grace")
	assert.Equal(t, 1, chained)
}

func TestReconcileOrphanedHandoffs_InlineHandoffNoLivenessPastGrace_StillChains(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	cards := []ReconcileCard{{Key: "SC-5396", Comments: sc5476ThreadAt57(base)}}

	chained := 0
	nPast := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		LiveAgents:  nil,
		ChainReview: func(string) error { chained++; return nil },
	}, base.Add(56*time.Second+StuckRunningGrace+time.Minute))
	assert.Equal(t, 1, nPast)
	assert.Equal(t, 1, chained)

	chained = 0
	nInGrace := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		LiveAgents:  nil,
		ChainReview: func(string) error { chained++; return nil },
	}, base.Add(2*time.Minute))
	assert.Equal(t, 0, nInGrace)
	assert.Zero(t, chained)
}

func TestReconcileOrphanedHandoffs_InlineHandoffAlreadyReviewed_Leaves(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	comments := append(sc5476ThreadAt57(base), cmt("[human:review-complete]\nverdict: pass", base.Add(2*time.Minute)))
	cards := []ReconcileCard{{Key: "SC-5396", Comments: comments}}
	chained := 0
	n := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		LiveAgents:  liveAgents("board-SC-5396-implementation"),
		ChainReview: func(string) error { chained++; return nil },
	}, base.Add(3*time.Minute))
	assert.Equal(t, 0, n, "handoffAwaitsReview is false, exactly as before the change")
	assert.Zero(t, chained)
}

func TestReconcileOrphanedHandoffs_PlainHandoff_StillChains(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	comments := []tracker.Comment{
		cmt(ImplementationStartedHeader, base.Add(-40*time.Minute)),
		cmt("[human:ready-for-review]\nbranch: autofix/sc-5396\ncommits: e5409b74, 56145a07", base.Add(56*time.Second)),
	}
	cards := []ReconcileCard{{Key: "SC-5396", Comments: comments}}
	chained := 0
	n := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		LiveAgents:  liveAgents("board-SC-5396-implementation"),
		ChainReview: func(string) error { chained++; return nil },
	}, base.Add(57*time.Second))
	assert.Equal(t, 1, n, "a live implementation container never suppressed a plain handoff and still does not")
	assert.Equal(t, 1, chained)
}

func TestHandleBoardAgentExit_InlineHandoffLiveContainer_ChainsNoSecondReview(t *testing.T) {
	withInstantBoardExitRecheck(t)
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	c := &syncCommenter{comments: sc5476ThreadAt57(base)}
	var chained bool
	handleBoardAgentExit(context.Background(), nil, hookevents.Event{
		AgentName: "board-SC-5396-implementation", EventName: "Stop",
	}, FailureDeps{
		CommenterFor: func() (tracker.Commenter, error) { return c, nil },
		ChainReview:  func(string) error { chained = true; return nil },
		LiveAgents:   liveAgents("board-SC-5396-implementation"),
		Reachable:    alwaysReachable,
		Logger:       zerolog.Nop(),
	})
	assert.False(t, chained, "an intermediate Stop while the inline reviewer's container is alive must not chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "and must post nothing on the ticket")
}

func TestHandleBoardAgentExit_PlainHandoff_StillChains(t *testing.T) {
	withInstantBoardExitRecheck(t)
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	c := &syncCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, base.Add(-40*time.Minute)),
		cmt("[human:ready-for-review]\nbranch: autofix/sc-5396\ncommits: e5409b74, 56145a07", base.Add(56*time.Second)),
	}}
	var chained bool
	handleBoardAgentExit(context.Background(), nil, hookevents.Event{
		AgentName: "board-SC-5396-implementation", EventName: "Stop",
	}, FailureDeps{
		CommenterFor: func() (tracker.Commenter, error) { return c, nil },
		ChainReview:  func(string) error { chained = true; return nil },
		LiveAgents:   liveAgents("board-SC-5396-implementation"),
		Reachable:    alwaysReachable,
		Logger:       zerolog.Nop(),
	})
	assert.True(t, chained, "the executor's plain handoff must still be chained")
}
