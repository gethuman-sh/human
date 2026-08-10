package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/tracker"
)

// SC-3857: a failed card can be declared dead twice, and the second time
// re-dates it. handleBoardAgentExit posted a stage's *-failed marker for any
// exit that did not leave the stage settled, without asking whether the stage
// had already failed with no relaunch since — so a second, unrelated exit
// event for the same dead run (e.g. a reap-synthesized StopFailure racing an
// already-recorded genuine failure) re-posted the terminal marker and re-dated
// the card as if it had just broken.
//
// This is deliberately not a global dedup: a genuine second failure AFTER a
// relaunch (evidenced by a fresh *-started marker newer than the failure)
// must still post and must still re-date (AD4).

// stageStartedHeaderFor mirrors failedHeaderFor for the *-started marker, so
// the table below can build "started, then failed" threads generically.
func stageStartedHeaderFor(stage BoardStage) string {
	switch stage {
	case BoardPlanning:
		return PlanningStartedHeader
	case BoardImplementation:
		return ImplementationStartedHeader
	case BoardVerification:
		return ReviewStartedHeader
	default:
		return ""
	}
}

func TestHandleBoardAgentExit_AlreadyFailedStagePostsNothing(t *testing.T) {
	cases := []struct {
		name      string
		stage     BoardStage
		agent     string
		errorType string
		event     string
	}{
		{"implementation generic exit", BoardImplementation, "board-SC-1-implementation", "", "StopFailure"},
		{"planning generic exit", BoardPlanning, "board-SC-1-planning", "", "StopFailure"},
		{"verification generic exit", BoardVerification, "board-SC-1-verification", "", "StopFailure"},
		{"needs-person wall", BoardImplementation, "board-SC-1-implementation", "authentication_error", "StopFailure"},
		{"silence reap", BoardImplementation, "board-SC-1-implementation", ReapSilenceErrorType + ":10m0s", "StopFailure"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withInstantBoardExitRecheck(t)
			started := stageStartedHeaderFor(tc.stage)
			failed := failedHeaderFor(tc.stage)
			require.NotEmpty(t, started)
			require.NotEmpty(t, failed)

			c := &syncCommenter{comments: []tracker.Comment{
				cmt(started, time.Unix(1, 0)),
				cmt(failed+"\nsome earlier diagnosis", time.Unix(2, 0)),
			}}
			commenterFor := func() (tracker.Commenter, error) { return c, nil }
			retry := StageRetry{
				Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
				Attempts: func(string, BoardStage) (int, error) { return 1, nil },
				Relaunch: func(_ string, s BoardStage) (bool, error) { return true, nil },
			}

			handleBoardAgentExit(context.Background(), nil,
				hookevents.Event{AgentName: tc.agent, ErrorType: tc.errorType, EventName: tc.event},
				FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: retry, Logger: zerolog.Nop()})

			c.mu.Lock()
			defer c.mu.Unlock()
			assert.Empty(t, c.added, "a stage already failed with no relaunch since must not be told a second time")
		})
	}
}

// The other half: after a relaunch (a *-started marker newer than the
// failure), a genuine second failure still posts.
func TestHandleBoardAgentExit_FailureAfterARelaunchStillPosts(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1, 0)),
		cmt(ImplementationFailedHeader+"\nfirst diagnosis", time.Unix(2, 0)),
		cmt(ImplementationStartedHeader, time.Unix(3, 0)),
	}}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	// Relaunch refused (nothing launched): isolates the assertion to the
	// failure post itself, without the separate "Automatic retry N/M" note a
	// successful relaunchBounded also posts.
	retry := StageRetry{
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { return false, nil },
	}

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-implementation", EventName: "StopFailure"},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: retry, Logger: zerolog.Nop()})

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1, "a genuine failure after a relaunch must still post")
	assert.True(t, strings.HasPrefix(c.added[0], ImplementationFailedHeader))
}

// A silence-reap exit onto an already-failed stage with no relaunch since must
// neither post nor spend the ticket's automatic-retry or attempt budget.
func TestHandleBoardAgentExit_AlreadyFailedStageChargesNoBudget(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1, 0)),
		cmt(ImplementationFailedHeader+"\n"+silenceReapReason("5m0s"), time.Unix(2, 0)),
	}}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var attemptsCalled, relaunchCalled bool
	retry := StageRetry{
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { attemptsCalled = true; return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunchCalled = true; return true, nil },
	}

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-implementation", ErrorType: ReapSilenceErrorType + ":18m0s", EventName: "StopFailure"},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: retry, Logger: zerolog.Nop()})

	assert.False(t, attemptsCalled, "an already-failed stage must not charge the attempt counter")
	assert.False(t, relaunchCalled, "an already-failed stage must not be relaunched again")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added)
}

// Pins SC-4026: escalatePRLoop already refuses to re-post its terminal marker
// onto a done stage that already carries [human:pr-review-failed] as its
// newest marker. Passes before this ticket's fix — recorded here as the half
// of the guard AD5 leaves to the earlier ticket rather than re-fixed.
func TestAdvancePRLoop_escalationOntoAnAlreadyFailedDoneStagePostsNothing(t *testing.T) {
	c := &fakeCommenter{comments: append(
		reviewStartedComments(1, "https://example/pr/7", 7, "feat/x"),
		cmt(PRReviewFailedHeader+"\nstopped before recording a verdict", time.Unix(9, 0)),
	)}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewRecorded: false}))

	assert.Empty(t, c.added, "the done stage already failed with no relaunch since — nothing more is posted")
}

// AD5/Change 4: AdvanceDeployFix's escalation must not re-post
// [human:deploy-failed] onto a done stage that already carries it as its
// newest marker with no relaunch since (evidenced by a fresh
// deploy-fix-started marker). Fails before Change 4.
func TestAdvanceDeployFix_escalationOntoAnAlreadyFailedDoneStagePostsNothing(t *testing.T) {
	c := &fakeCommenter{comments: append(
		deployFixReadyComments(),
		cmt(DeployFailedHeader+"\nan earlier escalation", time.Unix(9, 0)),
	)}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})

	err := deps.AdvanceDeployFix(context.Background(), "SC-1", ExitNeedsInput)
	require.NoError(t, err)

	assert.Empty(t, c.added, "the done stage already failed with no relaunch since — nothing more is posted")
}
