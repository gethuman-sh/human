package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// retryPolicyFor builds a policy that records what it was asked to do.
func retryPolicyFor(outcome string, recorded bool, relaunched *[]BoardStage, resets *[]BoardStage) StageRetry {
	attempts := 0
	return StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return outcome, recorded },
		Attempts: func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Reset:    func(_ string, s BoardStage) { *resets = append(*resets, s) },
		Relaunch: func(_ string, s BoardStage) error { *relaunched = append(*relaunched, s); return nil },
	}
}

// The behaviour change of A1: a stage that died with a retryable outcome posts
// its failed marker AND relaunches, instead of leaving the card red for a human
// to click Retry.
func TestHandleBoardAgentExit_RetryableFailureRelaunchesTheStage(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitRetryable, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor,
		nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Equal(t, []BoardStage{BoardPlanning}, relaunched)

	// The failure stays on the record: the retry transitions require a card that
	// derives to a failed state, and hiding the crash would make the trail lie.
	var sawFailedMarker bool
	for _, body := range c.added {
		if _, state, ok := ClassifyMarker(body); ok && state == BoardFailed {
			sawFailedMarker = true
		}
	}
	require.True(t, sawFailedMarker, "the failed marker must be posted before the relaunch")
}

// A stage that reported the substrate was down posts a distinct *-outage marker
// (not a red *-failed one) and does NOT relaunch on the live path — the durable
// reconcile pass owns the backoff relaunch (SC-2307).
func TestHandleBoardAgentExit_OutagePostsOutageMarkerAndDoesNotRelaunch(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitOutage, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor,
		nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Empty(t, relaunched, "the live path must not relaunch an outage — the reconcile backoff does")

	var sawOutage, sawFailed bool
	for _, body := range c.added {
		if _, state, ok := ClassifyMarker(body); ok {
			switch state {
			case BoardOutage:
				sawOutage = true
			case BoardFailed:
				sawFailed = true
			}
		}
	}
	require.True(t, sawOutage, "an outage exit must post its *-outage marker")
	require.False(t, sawFailed, "an outage must never red the card with a *-failed marker")
}

// The crash path is unchanged by the outage split: a retryable exit still reds
// the card with a *-failed marker and takes the bounded relaunch (SC-2307).
func TestHandleBoardAgentExit_CrashStillFailsBounded(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitRetryable, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor,
		nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
	var sawFailed, sawOutage bool
	for _, body := range c.added {
		if _, state, ok := ClassifyMarker(body); ok {
			switch state {
			case BoardFailed:
				sawFailed = true
			case BoardOutage:
				sawOutage = true
			}
		}
	}
	require.True(t, sawFailed, "a retryable crash still posts a *-failed marker")
	require.False(t, sawOutage, "a crash must not be mistaken for an outage")
}

// A stage that concluded it needs a human must not be relaunched — that is the
// difference between recovering from a flake and looping on a real blocker.
func TestHandleBoardAgentExit_TerminalFailureIsNotRelaunched(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitNeedsHumanWork, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor,
		nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Empty(t, relaunched)
}

// A clean finish clears the attempt budget, so a later unrelated failure gets a
// full one rather than the remainder of an older problem's.
func TestHandleBoardAgentExit_CleanFinishResetsTheRetryBudget(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(PlanReadyHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitDone, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor,
		nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Equal(t, []BoardStage{BoardPlanning}, resets)
	require.Empty(t, relaunched, "a clean finish is not a failure to retry")
}
