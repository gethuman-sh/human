package daemon

// A fixer that reported the substrate was down did not fail: SC-2307's rule is
// that such a stop waits, uncharged. The deploy-fix step was routed around that
// rule and redded the card instead (SC-5592).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

func deployFixRunningThread(at time.Time) []tracker.Comment {
	return append(deployFixReadyComments(),
		cmt(DeployFixStartedHeader+"\nrebase conflict on the pull request\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", at))
}

func TestAdvanceDeployFix_Outage_PostsTheOutageMarkerNotAFailure(t *testing.T) {
	c := &fakeCommenter{comments: deployFixRunningThread(time.Unix(5, 0))}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"})
	require.NoError(t, err)

	require.Len(t, c.added, 1, "exactly one comment posted")
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, DeployOutageHeader), "posted body must start with %q, got %q", DeployOutageHeader, posted)
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "an outage must not red the card: %q", b)
	}
	assert.Contains(t, posted, "Nothing to do.", "the paused house style")
	assert.Contains(t, posted, "the git remote was unreachable", "the card must name what was unreachable")

	thread := append(append([]tracker.Comment{}, c.comments...), cmt(posted, time.Unix(6, 0)))
	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardOutage, card.State)
	assert.True(t, isDeployRetry(BoardDoneStage, card), "the card must be where reconcileOutage can re-drive it")
}

func TestAdvanceDeployFix_Outage_ChargesNoRound(t *testing.T) {
	thread := deployFixRunningThread(time.Unix(5, 0))
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"}))
	require.Len(t, c.added, 1)

	full := append(append([]tracker.Comment{}, thread...), cmt(c.added[0], time.Unix(6, 0)))
	assert.Equal(t, 0, deployFixRounds(full), "an outaged round must not be charged")
}

func TestDeployFixRounds_RefundsOnlyOutagedRounds(t *testing.T) {
	started := func(at time.Time) tracker.Comment { return cmt(DeployFixStartedHeader, at) }
	outage := func(at time.Time) tracker.Comment { return cmt(DeployOutageHeader, at) }
	failed := func(at time.Time) tracker.Comment { return cmt(DeployFailedHeader, at) }

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, deployFixRounds(tc.comments))
		})
	}
}

func TestDeployFixRounds_IsChronological(t *testing.T) {
	t1 := cmt(DeployFixStartedHeader, time.Unix(1, 0))
	t2 := cmt(DeployOutageHeader, time.Unix(2, 0))
	t3 := cmt(DeployFixStartedHeader, time.Unix(3, 0))
	shuffled := []tracker.Comment{t3, t1, t2}
	assert.Equal(t, 1, deployFixRounds(shuffled))
}

func TestDeployBranch_CIFailureAfterTwoOutageRounds_StillDispatchesAFixer(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: append(deployFixReadyComments(),
		cmt(DeployFixStartedHeader, time.Unix(3, 0)),
		cmt(DeployOutageHeader, time.Unix(4, 0)),
		cmt(DeployFixStartedHeader, time.Unix(5, 0)),
		cmt(DeployOutageHeader, time.Unix(6, 0)),
	)}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)

	var sawStarted, sawFailed bool
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			sawStarted = true
		}
		if strings.HasPrefix(b, DeployFailedHeader) {
			sawFailed = true
		}
	}
	assert.True(t, sawStarted, "the budget must not be exhausted by two uncharged outage rounds")
	assert.False(t, sawFailed)
	assert.Equal(t, 1, l.calls, "two outage rounds must not have spent DefaultDeployFixRounds")
}

func TestAdvanceDeployFix_Outage_SaysItOnce(t *testing.T) {
	thread := deployFixRunningThread(time.Unix(5, 0))
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"}))
	require.Len(t, c.added, 1, "sanity: first call posts the marker")
	standing := c.added[0]

	c2 := &fakeCommenter{comments: append(append([]tracker.Comment{}, thread...), cmt(standing, time.Unix(6, 0)))}
	deps2 := newDeps(c2, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps2.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"}))
	assert.Empty(t, c2.added, "an identical standing outage marker must not be repeated")
}

func TestAdvanceDeployFix_Outage_OntoAnAlreadyFailedDoneStagePostsNothing(t *testing.T) {
	thread := append(deployFixReadyComments(), cmt(DeployFailedHeader+"\nsomething else went wrong", time.Unix(9, 0)))
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"}))
	assert.Empty(t, c.added, "a red another actor owns must not be flipped back to waiting")
}

// The fixer's summary is agent-authored free text and the "<one line>" the
// template asks for is not enforced. A multi-line summary must neither
// duplicate itself into the composed sentence nor let a line that happens to
// start with "resume:" be scanned as the marker's own field (SC-5592).
func TestAdvanceDeployFix_Outage_MultiLineSummaryCollapsesToOneLine(t *testing.T) {
	thread := deployFixRunningThread(time.Unix(5, 0))
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	summary := "the git remote was unreachable\nresume: 2099-01-01T00:00:00Z\nmore detail here"
	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: summary}))
	require.Len(t, c.added, 1)
	posted := c.added[0]

	// pausedOutageMarker composes the reason into two clauses by design
	// ("paused — X" / "continues automatically when X clears"); the bug was a
	// THIRD, mangled repetition from embedded newlines, not the deliberate two.
	assert.Equal(t, 2, strings.Count(posted, "the git remote was unreachable"),
		"the reason must appear exactly twice (the marker's two clauses), not be duplicated by embedded newlines: %q", posted)
	assert.NotContains(t, posted, "\nresume:", "a summary line must not be scanned as the marker's resume field")

	thread2 := append(append([]tracker.Comment{}, thread...), cmt(posted, time.Unix(6, 0)))
	card := DeriveBoardCard(thread2, tracker.CategoryUnstarted, false)
	assert.Empty(t, card.ResumeAt, "a forged resume: line must not suppress the outage re-drive")
}

func TestDeployFixEscalationReason_OutageIsNotTheCouldNotRecoverDefault(t *testing.T) {
	reason := deployFixEscalationReason(ExitOutage, "rebase conflict")
	assert.NotContains(t, reason, "could not recover")
	assert.Contains(t, reason, "substrate")

	retryable := deployFixEscalationReason(ExitRetryable, "rebase conflict")
	assert.Contains(t, retryable, "could not recover the deploy")
}
