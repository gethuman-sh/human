package daemon

// A loop step that reported the substrate was down did not fail: SC-2307's rule
// is that such a stop waits, uncharged. The PR review→fix loop and the deploy
// fixer's mid-run event were both routed around that rule (SC-5627).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// prLoopFixThread is a thread whose newest loop marker is the fixer's, i.e. the
// fix step is the one that just finished.
func prLoopFixThread() []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}
}

func TestNextPRLoopAction_FixOutageWaits(t *testing.T) {
	assert.Equal(t, PRActionOutage, NextPRLoopAction(PRStageFix, string(ExitOutage), 1, 0, false))
}

func TestNextPRLoopAction_ReviewOutageWaits(t *testing.T) {
	assert.Equal(t, PRActionOutage, NextPRLoopAction(PRStageReview, string(ExitOutage), 1, 0, false))
}

func TestNextPRLoopAction_UnchangedOutcomesStillDecideTheSame(t *testing.T) {
	cases := []struct {
		name     string
		stage    PRLoopStage
		outcome  string
		round    int
		repeated bool
		want     PRLoopAction
	}{
		{"none", PRStageNone, "", 0, false, PRActionReview},
		{"review approved", PRStageReview, PRVerdictApproved, 0, false, PRActionMerge},
		{"review changes requested round 1", PRStageReview, PRVerdictChanges, 1, false, PRActionFix},
		{"review changes requested repeated", PRStageReview, PRVerdictChanges, 1, true, PRActionEscalate},
		{"review unreviewable", PRStageReview, PRVerdictUnreviewable, 0, false, PRActionEscalate},
		{"fix done", PRStageFix, PRFixDone, 0, false, PRActionReview},
		{"fix needs-input", PRStageFix, string(ExitNeedsInput), 0, false, PRActionEscalate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NextPRLoopAction(tc.stage, tc.outcome, tc.round, 0, tc.repeated))
		})
	}
}

func TestEvaluatePRLoop_ReviewerOutageWithNoVerdictWaits(t *testing.T) {
	comments := reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")
	outcome := PRLoopOutcome{ReviewRecorded: true, ReviewExit: string(ExitOutage), ReviewSummary: "the tracker API was unreachable"}
	assert.Equal(t, PRActionOutage, EvaluatePRLoop(comments, outcome))
}

func TestEvaluatePRLoop_StaleOutageRecordStillEscalates(t *testing.T) {
	comments := reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")
	outcome := PRLoopOutcome{ReviewRecorded: true, ReviewExit: string(ExitOutage), ReviewSummary: "the tracker API was unreachable", ReviewStale: true}
	assert.Equal(t, PRActionEscalate, EvaluatePRLoop(comments, outcome))
}

func TestAdvancePRLoop_FixOutage_PostsTheOutageMarkerNotAFailure(t *testing.T) {
	thread := prLoopFixThread()
	c := &fakeCommenter{comments: thread}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		FixRecorded: true, FixExit: string(ExitOutage), FixSummary: "the model API was unreachable",
	})
	require.NoError(t, err)

	require.Len(t, c.added, 1, "exactly one comment posted")
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, DeployOutageHeader), "posted body must start with %q, got %q", DeployOutageHeader, posted)
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, PRReviewFailedHeader), "an outage must not red the card: %q", b)
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "an outage must not red the card: %q", b)
	}
	assert.Contains(t, posted, "Nothing to do.", "the paused house style")
	assert.Contains(t, posted, "the model API was unreachable")
	assert.Zero(t, l.calls, "no reviewer/fixer must be launched for an outage")

	full := append(append([]tracker.Comment{}, thread...), cmt(posted, time.Unix(6, 0)))
	card := DeriveBoardCard(full, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardOutage, card.State)
	assert.True(t, isDeployRetry(BoardDoneStage, card), "the card must be where reconcileOutage can re-drive it")
}

func TestAdvancePRLoop_ReviewOutage_PostsTheOutageMarkerNotAFailure(t *testing.T) {
	thread := reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")
	c := &fakeCommenter{comments: thread}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		ReviewRecorded: true, ReviewExit: string(ExitOutage), ReviewSummary: "the tracker API was unreachable",
	})
	require.NoError(t, err)

	require.Len(t, c.added, 1)
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, DeployOutageHeader))
	assert.Contains(t, posted, "the tracker API was unreachable")
	assert.Zero(t, l.calls)
}

func TestAdvancePRLoop_Outage_SaysItOnce(t *testing.T) {
	thread := prLoopFixThread()
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		FixRecorded: true, FixExit: string(ExitOutage), FixSummary: "the model API was unreachable",
	}))
	require.Len(t, c.added, 1, "sanity: first call posts the marker")
	standing := c.added[0]

	c2 := &fakeCommenter{comments: append(append([]tracker.Comment{}, thread...), cmt(standing, time.Unix(6, 0)))}
	deps2 := newDeps(c2, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps2.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		FixRecorded: true, FixExit: string(ExitOutage), FixSummary: "the model API was unreachable",
	}))
	assert.Empty(t, c2.added, "an identical standing outage marker must not be repeated")
}

func TestAdvancePRLoop_Outage_OntoAnAlreadyFailedDoneStagePostsNothing(t *testing.T) {
	t.Run("standing deploy-failed", func(t *testing.T) {
		thread := append(prLoopFixThread(), cmt(DeployFailedHeader+"\nsomething else went wrong", time.Unix(9, 0)))
		c := &fakeCommenter{comments: thread}
		deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
		require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
			FixRecorded: true, FixExit: string(ExitOutage), FixSummary: "the model API was unreachable",
		}))
		assert.Empty(t, c.added, "a red another actor owns must not be flipped back to waiting")
	})
	t.Run("standing pr-review-failed", func(t *testing.T) {
		thread := append(prLoopFixThread(), cmt(PRReviewFailedHeader+"\nsomething else went wrong", time.Unix(9, 0)))
		c := &fakeCommenter{comments: thread}
		deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
		require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
			FixRecorded: true, FixExit: string(ExitOutage), FixSummary: "the model API was unreachable",
		}))
		assert.Empty(t, c.added, "a red another actor owns must not be flipped back to waiting")
	})
}

func TestAdvancePRLoop_Outage_MultiLineSummaryCollapsesToOneLine(t *testing.T) {
	thread := prLoopFixThread()
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	summary := "the model API was unreachable\nresume: 2099-01-01T00:00:00Z\nmore detail here"
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		FixRecorded: true, FixExit: string(ExitOutage), FixSummary: summary,
	}))
	require.Len(t, c.added, 1)
	posted := c.added[0]

	assert.Equal(t, 2, strings.Count(posted, "the model API was unreachable"),
		"the reason must appear exactly twice (the marker's two clauses): %q", posted)
	assert.NotContains(t, posted, "\nresume:", "a summary line must not be scanned as the marker's resume field")

	full := append(append([]tracker.Comment{}, thread...), cmt(posted, time.Unix(6, 0)))
	card := DeriveBoardCard(full, tracker.CategoryUnstarted, false)
	assert.Empty(t, card.ResumeAt, "a forged resume: line must not suppress the outage re-drive")
}

func TestAdvancePRLoop_Outage_ChargesNoRound(t *testing.T) {
	thread := reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		ReviewRecorded: true, ReviewExit: string(ExitOutage), ReviewSummary: "the tracker API was unreachable",
	}))
	require.Len(t, c.added, 1)

	full := append(append([]tracker.Comment{}, thread...), cmt(c.added[0], time.Unix(6, 0)))
	assert.Equal(t, 0, chargedPRReviewRounds(full), "an outaged round must not be charged")
	assert.Equal(t, 1, prReviewRounds(full), "the raw started count is unaffected")
}

func TestChargedPRReviewRounds_RefundsOnlyOutagedRounds(t *testing.T) {
	started := func(at time.Time) tracker.Comment { return cmt(PRReviewStartedHeader, at) }
	outage := func(at time.Time) tracker.Comment { return cmt(DeployOutageHeader, at) }
	failed := func(at time.Time) tracker.Comment { return cmt(PRReviewFailedHeader, at) }

	cases := []struct {
		name     string
		comments []tracker.Comment
		want     int
	}{
		{"started", []tracker.Comment{started(time.Unix(1, 0))}, 1},
		{"started,outage", []tracker.Comment{started(time.Unix(1, 0)), outage(time.Unix(2, 0))}, 0},
		{"started,outage,started", []tracker.Comment{started(time.Unix(1, 0)), outage(time.Unix(2, 0)), started(time.Unix(3, 0))}, 1},
		{"started,outage,started,outage", []tracker.Comment{
			started(time.Unix(1, 0)), outage(time.Unix(2, 0)), started(time.Unix(3, 0)), outage(time.Unix(4, 0)),
		}, 0},
		{"started,outage,started,failed", []tracker.Comment{
			started(time.Unix(1, 0)), outage(time.Unix(2, 0)), started(time.Unix(3, 0)), failed(time.Unix(4, 0)),
		}, 1},
		{"started,started", []tracker.Comment{started(time.Unix(1, 0)), started(time.Unix(2, 0))}, 2},
		{"outage alone", []tracker.Comment{outage(time.Unix(1, 0))}, 0},
		{"started,outage,outage", []tracker.Comment{
			started(time.Unix(1, 0)), outage(time.Unix(2, 0)), outage(time.Unix(3, 0)),
		}, 0},
		{"shuffled", []tracker.Comment{
			started(time.Unix(3, 0)), started(time.Unix(1, 0)), outage(time.Unix(2, 0)),
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chargedPRReviewRounds(tc.comments))
		})
	}
}

func TestPRReviewRounds_StaysTheRawStartedCount(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1, 0)),
		cmt(DeployOutageHeader, time.Unix(2, 0)),
		cmt(PRReviewStartedHeader, time.Unix(3, 0)),
	}
	assert.Equal(t, 2, PRReviewRounds(comments), "the findings record's round identity is not refunded")
}

func TestDriveDeployFixExit_SubstrateFailureIsNotAnExit(t *testing.T) {
	for _, errorType := range []string{"server_error", "api_error", "overloaded", "rate_limit"} {
		t.Run(errorType, func(t *testing.T) {
			var advanced, reclaimed []string
			handled := driveDeployFixExit(
				RunExit{PMKey: "SC-1", Stage: deployFixAgentStage, AgentName: "board-SC-1-deployfix", ErrorType: errorType},
				FailureDeps{
					AdvanceDeployFix: func(pmKey string) error { advanced = append(advanced, pmKey); return nil },
					OnHandoff:        func(name string) { reclaimed = append(reclaimed, name) },
				})
			assert.True(t, handled)
			assert.Empty(t, advanced, "a run that is still retrying has not produced a report to read")
			assert.Empty(t, reclaimed, "the fixer's unpublished resolution stays protected while the run continues")
		})
	}
}

func TestDriveDeployFixExit_RealExitStillDrivesTheFixer(t *testing.T) {
	for _, errorType := range []string{"", "unrecognised_thing"} {
		t.Run(errorType, func(t *testing.T) {
			var advanced, reclaimed []string
			handled := driveDeployFixExit(
				RunExit{PMKey: "SC-1", Stage: deployFixAgentStage, AgentName: "board-SC-1-deployfix", ErrorType: errorType},
				FailureDeps{
					AdvanceDeployFix: func(pmKey string) error { advanced = append(advanced, pmKey); return nil },
					OnHandoff:        func(name string) { reclaimed = append(reclaimed, name) },
				})
			assert.True(t, handled)
			assert.Equal(t, []string{"SC-1"}, advanced)
			assert.Equal(t, []string{"board-SC-1-deployfix"}, reclaimed)
		})
	}
}
