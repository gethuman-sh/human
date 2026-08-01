package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A stage launched a long time after the previous stage finished must leave an
// attributed wait record on the ticket (SC-2462): the gap between a done marker
// and the next stage's launch is otherwise invisible.
func TestStartAgentStage_recordsWaitOverThreshold(t *testing.T) {
	restore := StageWaitThreshold
	StageWaitThreshold = 5 * time.Minute
	t.Cleanup(func() { StageWaitThreshold = restore })

	eligibleAt := time.Now().Add(-31 * time.Minute)
	c := &fakeCommenter{comments: []tracker.Comment{
		{ID: "1", Body: ReadyForReviewHeader, Created: eligibleAt},
	}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardVerification,
		Cause: WaitCausePollBoundary,
	})
	require.NoError(t, err)

	var wait string
	for _, b := range c.added {
		if strings.HasPrefix(strings.TrimSpace(b), StageWaitHeader) {
			wait = b
		}
	}
	require.NotEmpty(t, wait, "expected a [human:stage-wait] record for a 31-minute inter-stage gap")
	assert.Contains(t, wait, "stage: "+string(BoardVerification))
	assert.Contains(t, wait, "cause: "+string(WaitCausePollBoundary))
	assert.Contains(t, wait, "eligible: ")
	assert.Contains(t, wait, "waited: ")
	// The wait record must never classify as a board stage/state.
	_, _, ok := ClassifyMarker(wait)
	assert.False(t, ok, "[human:stage-wait] must stay out of orderedMarkerSpecs")
}

// A stage that follows the previous one promptly produces no record — this is a
// record of waiting, not a heartbeat (SC-2462).
func TestStartAgentStage_silentUnderThreshold(t *testing.T) {
	restore := StageWaitThreshold
	StageWaitThreshold = 5 * time.Minute
	t.Cleanup(func() { StageWaitThreshold = restore })

	c := &fakeCommenter{comments: []tracker.Comment{
		{ID: "1", Body: ReadyForReviewHeader, Created: time.Now().Add(-20 * time.Second)},
	}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardVerification,
		Cause: WaitCausePollBoundary,
	})
	require.NoError(t, err)

	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(strings.TrimSpace(b), StageWaitHeader),
			"a promptly-chained stage must post no wait record")
	}
}

// A human-initiated drop (empty cause) is think-time, not a pipeline wait, so it
// is never recorded even over the threshold.
func TestStartAgentStage_manualDropSuppressed(t *testing.T) {
	restore := StageWaitThreshold
	StageWaitThreshold = 5 * time.Minute
	t.Cleanup(func() { StageWaitThreshold = restore })

	c := &fakeCommenter{comments: []tracker.Comment{
		{ID: "1", Body: ReadyForReviewHeader, Created: time.Now().Add(-40 * time.Minute)},
	}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardVerification, // no Cause
	})
	require.NoError(t, err)

	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(strings.TrimSpace(b), StageWaitHeader),
			"a manual drop (empty cause) must post no wait record")
	}
}

func TestEligibleAnchor_newestDoneMarker(t *testing.T) {
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := time.Now().Add(-1 * time.Hour)
	comments := []tracker.Comment{
		{ID: "1", Body: ReadyForReviewHeader, Created: t1},
		{ID: "2", Body: ReviewCompleteHeader, Created: t2},
		{ID: "3", Body: ReviewStartedHeader, Created: time.Now()},
	}
	got, ok := eligibleAnchor(comments)
	require.True(t, ok)
	assert.WithinDuration(t, t2, got, time.Second)
}

func TestEligibleAnchor_noneWhenNoDoneMarker(t *testing.T) {
	comments := []tracker.Comment{
		{ID: "1", Body: ImplementationStartedHeader, Created: time.Now()},
		{ID: "2", Body: ImplementationFailedHeader, Created: time.Now()},
	}
	_, ok := eligibleAnchor(comments)
	assert.False(t, ok)
}

func TestRecordStageWait_suppressedWhenNoAnchor(t *testing.T) {
	restore := StageWaitThreshold
	StageWaitThreshold = 5 * time.Minute
	t.Cleanup(func() { StageWaitThreshold = restore })

	c := &fakeCommenter{comments: []tracker.Comment{
		{ID: "1", Body: ImplementationStartedHeader, Created: time.Now().Add(-40 * time.Minute)},
	}, nextID: 1}

	posted := recordStageWait(context.Background(), c, "SC-1", BoardVerification, c.comments, WaitCausePollBoundary, "", zerolog.Nop())
	assert.False(t, posted)
	assert.Empty(t, c.added)
}

// A stall that followed a recorded wait is attributable, not judged from
// silence: the stuck-running red names the wait's cause when the thread carries
// one (SC-2462).
func TestReconcileStuckRunning_annotatesWaitCause(t *testing.T) {
	now := time.Unix(10_000, 0)
	staleStart := now.Add(-StuckRunningGrace - time.Minute)
	waitBody := StampDaemon(composeStageWait(BoardVerification, staleStart.Add(-time.Hour), staleStart, WaitCausePollBoundary), "")
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(waitBody, staleStart),
			cmt(ReviewStartedHeader, staleStart),
		},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), liveAgents(), capturingPoster(&posted), StageRetry{}, nil, nil, "", now, zerolog.Nop())

	require.Equal(t, 1, n)
	require.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, ReviewFailedHeader))
	assert.Contains(t, posted[0].Body, "after wait cause: "+string(WaitCausePollBoundary))
}
