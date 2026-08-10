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

// SC-4244: a launch the single-flight guard refused started nothing, so nothing
// on the ticket may claim it did. These pin the three launch sites, the retry
// accounting behind them, and the two clocks a false started marker used to
// re-date — the stuck-running grace and the late-result reconciler.

// The claim is still posted (it is unclassified and does not move the clock);
// the started marker is not, because no run began.
func TestStartAgentStage_RefusalPostsNoStartedMarker(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: ErrAgentAlreadyRunning}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "daemon-1"

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning}))

	require.Equal(t, 1, l.calls, "the launch is attempted; the launcher is what refuses it")
	for _, body := range c.added {
		assert.NotContains(t, body, PlanningStartedHeader, "no run started, so nothing may say one did")
		assert.NotContains(t, body, PlanningFailedHeader, "a refusal is not a failure")
	}
}

// The bool is what the retry path keys off, so it is asserted directly and not
// only through its consequences.
func TestStartAgentStage_RefusalReportsNotLaunched(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(PlanningFailedHeader, time.Unix(1, 0))}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	launched, err := deps.ApplyRetryTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardPlanning})

	require.NoError(t, err, "a refusal is benign")
	assert.False(t, launched)
}

// The wait record is the started marker's companion: a stage that never began
// waited for nothing, so neither is written.
func TestStartAgentStage_RefusalRecordsNoStageWait(t *testing.T) {
	restore := StageWaitThreshold
	StageWaitThreshold = 5 * time.Minute
	t.Cleanup(func() { StageWaitThreshold = restore })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanReadyHeader, time.Unix(1, 0)),
	}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	_, err := deps.ApplyRetryTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation, Cause: WaitCausePollBoundary,
	})

	require.NoError(t, err)
	for _, body := range c.added {
		assert.NotContains(t, body, StageWaitHeader)
		assert.NotContains(t, body, ImplementationStartedHeader)
	}
}

// The surviving SC-2462 ordering: the wait is measured from the thread as it
// stood BEFORE the launch, and is still recorded ahead of the started marker.
func TestStartAgentStage_LaunchedPostsWaitThenStarted(t *testing.T) {
	restore := StageWaitThreshold
	StageWaitThreshold = 5 * time.Minute
	t.Cleanup(func() { StageWaitThreshold = restore })

	c := &fakeCommenter{comments: []tracker.Comment{
		{ID: "1", Body: PlanReadyHeader, Created: time.Now().Add(-31 * time.Minute)},
	}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	_, err := deps.ApplyRetryTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation, Cause: WaitCausePollBoundary,
	})

	require.NoError(t, err)
	require.Len(t, c.added, 2)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(c.added[0]), StageWaitHeader))
	assert.Equal(t, ImplementationStartedHeader, c.added[1])
}

// End to end through the real transition engine: the refusal must reach
// relaunchBounded as launched=false so the charged attempt is rolled back and
// no "Automatic retry" note claims a round that never ran (SC-2989).
func TestRelaunchBounded_SingleFlightRefusalRollsBackTheAttempt(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(PlanningFailedHeader, time.Unix(1, 0))}, nextID: 1}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	rec := &retryRecorder{}
	policy := rec.policy(ExitRetryable, true)
	policy.Relaunch = func(pmKey string, s BoardStage) (bool, error) {
		return deps.ApplyRetryTransition(context.Background(),
			BoardTransitionRequest{PMKey: pmKey, From: s, To: s})
	}

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardPlanning, rec, "daemon-1", zerolog.Nop())

	require.False(t, ok, "nothing was relaunched")
	assert.Zero(t, rec.attempts, "the charged attempt is rolled back")
	assert.Equal(t, 1, rec.uncounts)
	assert.Empty(t, rec.comments, "no Automatic-retry note for a launch that did not happen")
	for _, body := range c.added {
		assert.NotContains(t, body, PlanningStartedHeader)
	}
}

func TestLaunchPRLoopAgent_RefusalPostsNoStartedMarker(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: ErrAgentAlreadyRunning}
	deps := newDeps(c, l, &fakeDeployer{})

	launched, err := deps.launchPRLoopAgent(context.Background(), "SC-1", prFixAgentStage,
		"/human-pr-fix SC-1", PRFixStartedHeader)

	require.NoError(t, err)
	assert.False(t, launched)
	assert.Equal(t, 1, l.calls)
	assert.Empty(t, c.added, "neither a step that did not run nor a failure that did not happen")
}

// A refused fixer must not advance the loop's stage or spend a review round:
// the step that is actually running drives the next action from its own exit.
func TestAdvancePRLoop_RefusedFixLaunchLeavesLoopStageUnchanged(t *testing.T) {
	comments := []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(PRReviewStartedHeader+"\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", time.Unix(2, 0)),
	}
	c := &fakeCommenter{comments: comments, nextID: 2}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	roundsBefore := prReviewRounds(c.comments)
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: "changes-requested", ReviewRecorded: true}))

	for _, body := range c.added {
		assert.NotContains(t, body, PRFixStartedHeader)
	}
	assert.Equal(t, PRStageReview, latestPRLoopStage(c.comments), "the loop still stands where the running step left it")
	assert.Equal(t, roundsBefore, prReviewRounds(c.comments), "a step that did not run costs no round")
}

func TestOpenDraftPRAndReview_RefusalPostsNoStartedMarker(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, p)

	require.NoError(t, deps.openDraftPRAndReview(context.Background(), "SC-1", BoardCard{Branch: "feat/x"}))

	for _, body := range c.added {
		assert.NotContains(t, body, PRReviewStartedHeader)
	}
}

// A refused dispatch must not spend a deploy-fix round: the budget bounds
// rounds that ran, and the fixer already running re-drives the gate itself.
func TestDispatchDeployFixer_RefusalPostsNoStartedMarker(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: ErrAgentAlreadyRunning}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.dispatchDeployFixer(context.Background(), "SC-1",
		PRResult{URL: "https://example/pr/7", Number: 7}, "feat/x", "CI failed"))

	assert.Equal(t, 1, l.calls)
	assert.Empty(t, c.added)
	assert.Zero(t, deployFixRounds(c.comments), "no round was spent on a fixer nobody started")
}

// The answer is recorded and consumed either way; with no started marker to
// supersede it the card reads queued, which is the truthful placement, and
// reconcileQueuedLaunch is what starts it.
func TestPursueDecision_RefusalLeavesTheCardQueued(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanReadyHeader, base),
		cmt(ImplementationStartedHeader, base.Add(time.Second)),
		cmt(optionsBody, base.Add(time.Minute)),
	}, nextID: 3}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"}))

	var chosen bool
	for _, body := range c.added {
		if strings.HasPrefix(strings.TrimSpace(body), OptionChosenHeader) {
			chosen = true
		}
		assert.NotContains(t, body, ImplementationStartedHeader)
	}
	assert.True(t, chosen, "the answer is recorded even when the launch it asks for is refused")
	assert.Equal(t, BoardQueued, DeriveBoardCard(c.comments, tracker.CategoryUnstarted, false).State)
}

// The bug itself: a refused relaunch must not re-date the stage clock, or the
// stuck-running pass keeps buying the hung agent another grace.
func TestStuckGrace_RefusedRelaunchDoesNotMoveStageEnteredAt(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanReadyHeader, start.Add(-time.Minute)),
		cmt(ImplementationStartedHeader, start),
		cmt(ImplementationFailedHeader, start.Add(time.Minute)),
	}, nextID: 3}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	before := DeriveBoardCard(c.comments, tracker.CategoryUnstarted, false).StageEnteredAt

	_, err := deps.ApplyRetryTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)

	after := DeriveBoardCard(c.comments, tracker.CategoryUnstarted, false).StageEnteredAt
	assert.True(t, after.Equal(before), "the clock still points at the run that is actually running")
	assert.Greater(t, time.Since(after), StuckRunningGrace, "so the grace is reachable at last")
}

// With no started marker to clear it, the pending failure stands and a late
// result from the run that never stopped is still reported (SC-3853).
func TestLateResultCandidates_RefusedRelaunchKeepsThePendingFailure(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanReadyHeader, start.Add(-time.Minute)),
		cmt(ImplementationStartedHeader, start),
		cmt(ImplementationFailedHeader, start.Add(time.Minute)),
	}, nextID: 3}
	deps := newDeps(c, &fakeLauncher{err: ErrAgentAlreadyRunning}, &fakeDeployer{})

	_, err := deps.ApplyRetryTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)

	late := append(c.comments, cmt(ReadyForReviewHeader, start.Add(2*time.Minute)))
	require.NotEmpty(t, lateResultCandidates(late),
		"the failure was never superseded by a start, so the late success is a late result")
}
