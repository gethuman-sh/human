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

// TestHandleBoardAgentExit_AlreadyFailedStagePostsNothing pins the NARROW
// guard (SC-3857, revised): re-posting the stage's own *-failed marker onto a
// thread that already carries it is always suppressed, but the automatic
// in-place relaunch is not suppressed with it — it is decided independently,
// from deps.Retry.Outcome, which is unset (unrecorded) in every case here and
// so classifies as an undiagnosed crash needing a bounded relaunch (relaunchBounded).
// The two branches that own their own relaunch outright (needs-person, which
// never relaunches; silence-reap, whose post and relaunch are one atomic
// decision — see TestHandleBoardAgentExit_AlreadyFailedStageChargesNoBudget)
// still suppress both, and wantRelaunchNote is false for them.
func TestHandleBoardAgentExit_AlreadyFailedStagePostsNothing(t *testing.T) {
	cases := []struct {
		name             string
		stage            BoardStage
		agent            string
		errorType        string
		event            string
		wantRelaunchNote bool
	}{
		{"implementation generic exit", BoardImplementation, "board-SC-1-implementation", "", "StopFailure", true},
		{"planning generic exit", BoardPlanning, "board-SC-1-planning", "", "StopFailure", true},
		{"verification generic exit", BoardVerification, "board-SC-1-verification", "", "StopFailure", true},
		{"needs-person wall", BoardImplementation, "board-SC-1-implementation", "authentication_error", "StopFailure", false},
		{"silence reap", BoardImplementation, "board-SC-1-implementation", ReapSilenceErrorType + ":10m0s", "StopFailure", false},
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
			for _, body := range c.added {
				assert.False(t, strings.HasPrefix(body, failed), "the terminal marker itself must never be told a second time: %q", body)
			}
			if tc.wantRelaunchNote {
				require.Len(t, c.added, 1, "the relaunch decision runs independently of the suppressed post")
				assert.Contains(t, c.added[0], "Automatic retry", "an unrecorded outcome still gets its bounded relaunch")
			} else {
				assert.Empty(t, c.added, "this ending owns its own relaunch and must not be relaunched again")
			}
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

// TestHandleBoardAgentExit_ReviewSkillOwnFailurePostStillRelaunches pins the
// regression a second-opinion probe found in the FIRST cut of the SC-3857
// guard: the ordinary, prompt-instructed ending IS a skill posting its own
// *-failed marker and recording its own stage.<stage> outcome before it exits
// (human-review-skill.md:18 et al) — so by the time this exit event reaches
// the daemon, the marker THIS SAME ending posted is already the stage's
// newest, indistinguishable from a stale duplicate by marker history alone.
// A guard that returns before deps.Retry.tryRelaunch runs (the original,
// over-broad cut) swallowed every one of these endings' automatic bounded
// relaunch — not a rare unrecorded-exit corner, the everyday retryable-review
// path. Narrowed: the duplicate POST is still suppressed, but the relaunch,
// driven by deps.Retry.Outcome rather than the marker thread, must still run.
func TestHandleBoardAgentExit_ReviewSkillOwnFailurePostStillRelaunches(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{comments: []tracker.Comment{
		cmt(ReviewStartedHeader, time.Unix(1, 0)),
		cmt(ReviewFailedHeader+"\nan honest, retryable stage failure", time.Unix(2, 0)),
	}}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var outcomeConsulted bool
	retry := StageRetry{
		Outcome: func(string, BoardStage) (StageExit, bool) {
			outcomeConsulted = true
			return ExitRetryable, true
		},
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { return true, nil },
	}

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-verification", EventName: "StopFailure"},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: retry, Logger: zerolog.Nop()})

	assert.True(t, outcomeConsulted, "the relaunch decision must still consult the stage's own recorded exit")
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.added {
		assert.False(t, strings.HasPrefix(body, ReviewFailedHeader), "the skill's own review-failed post must not be re-told")
	}
	require.Len(t, c.added, 1, "a recorded retryable exit still gets its automatic relaunch")
	assert.Contains(t, c.added[0], "Automatic retry", "the retry note is the visible evidence the relaunch actually ran")
	assert.Contains(t, c.added[0], "exit: retryable")
}

// A silence-reap exit onto an already-failed stage with no relaunch since must
// neither post nor spend the ticket's automatic-retry or attempt budget. The
// silence-reap handler posts its own marker and decides its own relaunch in
// one synchronous call (unlike the generic path above), so a marker that
// already predates this call can only be a fully-completed EARLIER reap
// cycle — never this same call's own post — and relaunching again would
// double-launch atop whatever that earlier cycle already started or refused.
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
