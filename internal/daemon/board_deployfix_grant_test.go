package daemon

// A person's Retry deploy on a card whose deploy-fix budget is spent must buy
// one more fixer round; the machine's own re-drive must not (SC-5595).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// spentBudgetThread is the ticket the bug was reported on: a reviewed branch,
// two deploy-fix rounds over the card's life, and a red deploy.
func spentBudgetThread() []tracker.Comment {
	return append(deployFixReadyComments(),
		cmt(DeployFixStartedHeader+"\nrebase conflict on the pull request\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", time.Unix(3, 0)),
		cmt(DeployFixStartedHeader+"\nrebase conflict on the pull request\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", time.Unix(5, 0)),
		cmt(DeployFailedHeader+"\nreason: CI checks failed on the pull request", time.Unix(7, 0)),
	)
}

func deployFixStartedCount(bodies []string) int {
	n := 0
	for _, b := range bodies {
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			n++
		}
	}
	return n
}

// The headline regression: click Retry deploy on the spent card, let the
// re-run hit the same code-fixable CI failure, and a THIRD fixer must be
// dispatched instead of an identical deploy-failed.
func TestDeployRetryAfterSpentBudget_DispatchesAThirdFixerRound(t *testing.T) {
	syncPRReview(t)
	syncDeploy(t)
	c := &fakeCommenter{comments: spentBudgetThread()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)

	// The person's gesture: a re-drop on Done for a card already in (done, failed).
	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage}))

	var grants int
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployRetryHeader) {
			grants++
		}
	}
	require.Equal(t, 1, grants, "the gesture must record one durable grant: %v", c.added)

	before := len(c.added)
	require.NoError(t, deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage}))

	posted := c.added[before:]
	assert.Equal(t, 1, deployFixStartedCount(posted), "the granted round must dispatch a fixer: %v", posted)
	for _, b := range posted {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "a granted round must not red the card: %q", b)
	}
	// The grant is consumed by the round it funded, so a second failure reds.
	assert.Equal(t, 0, deployFixGrants(c.comments), "the dispatched round must consume the grant")
}

// The inverse: the SAME thread, re-driven by the MACHINE (the shippable
// re-drive and StageRetry both enter here), grants nothing and reds — with a
// reason that names the spent budget and what a person does instead.
func TestMachineRedriveAfterSpentBudget_RedsAndNamesTheBudget(t *testing.T) {
	syncPRReview(t)
	syncDeploy(t)
	c := &fakeCommenter{comments: spentBudgetThread()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)

	_, err := deps.ApplyRetryTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})
	require.NoError(t, err)
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployRetryHeader), "the machine must not grant itself a round: %q", b)
	}

	before := len(c.added)
	require.Error(t, deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage}))
	posted := c.added[before:]
	require.Equal(t, 0, deployFixStartedCount(posted), "a spent budget with no grant dispatches nothing")

	var failed string
	for _, b := range posted {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed, "the card must red: %v", posted)
	assert.Contains(t, failed, "automated fix budget for this ticket is spent")
	assert.Contains(t, failed, "Retry deploy")
}

// grantMarker / grantFundedStart / budgetStart / outage are the builders the
// arithmetic and provenance tests below share, mirroring
// TestDeployFixRounds_RefundsOnlyOutagedRounds's local builders.
func grantMarker(at time.Time) tracker.Comment { return cmt(DeployRetryHeader, at) }

func grantFundedStart(at time.Time) tracker.Comment {
	return cmt(markerBody(marker.Marker{
		Type: MarkerDeployFixStarted,
		Fields: fields("pr", "https://example/pr/7", "number", "7", "branch", "feat/x",
			DeployFixGrantField, deployFixGrantValue),
		Body: "CI checks failed on the pull request",
	}, "pr", "number", "branch", DeployFixGrantField), at)
}

func budgetStart(at time.Time) tracker.Comment {
	return cmt(markerBody(marker.Marker{
		Type:   MarkerDeployFixStarted,
		Fields: fields("pr", "https://example/pr/7", "number", "7", "branch", "feat/x"),
		Body:   "CI checks failed on the pull request",
	}, "pr", "number", "branch"), at)
}

func outage(at time.Time) tracker.Comment { return cmt(DeployOutageHeader, at) }

func TestDeployFixGrants_IsChronological(t *testing.T) {
	t1 := grantMarker(time.Unix(1, 0))
	t2 := grantFundedStart(time.Unix(2, 0))
	assert.Equal(t, 0, deployFixGrants([]tracker.Comment{t2, t1}),
		"a grant counted before the marker that spent it reads as unspent forever")
	assert.Equal(t, 0, deployFixGrants([]tracker.Comment{t1, t2}))
}

func TestDeployFixGrants_Arithmetic(t *testing.T) {
	cases := []struct {
		name     string
		comments []tracker.Comment
		want     int
	}{
		{"grant", []tracker.Comment{grantMarker(time.Unix(1, 0))}, 1},
		{"grant,grantFundedStart", []tracker.Comment{
			grantMarker(time.Unix(1, 0)), grantFundedStart(time.Unix(2, 0)),
		}, 0},
		{"grant,budgetStart", []tracker.Comment{
			grantMarker(time.Unix(1, 0)), budgetStart(time.Unix(2, 0)),
		}, 1},
		{"grant,grant", []tracker.Comment{
			grantMarker(time.Unix(1, 0)), grantMarker(time.Unix(2, 0)),
		}, 2},
		{"grant,grantFundedStart,grant", []tracker.Comment{
			grantMarker(time.Unix(1, 0)), grantFundedStart(time.Unix(2, 0)), grantMarker(time.Unix(3, 0)),
		}, 1},
		{"grantFundedStart alone", []tracker.Comment{grantFundedStart(time.Unix(1, 0))}, 0},
		{"budgetStart,budgetStart", []tracker.Comment{
			budgetStart(time.Unix(1, 0)), budgetStart(time.Unix(2, 0)),
		}, 0},
		{"nil", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, deployFixGrants(tc.comments))
		})
	}
}

func TestDeployFixGrants_OutageRefundsTheGrant(t *testing.T) {
	assert.Equal(t, 1, deployFixGrants([]tracker.Comment{
		grantMarker(time.Unix(1, 0)), grantFundedStart(time.Unix(2, 0)), outage(time.Unix(3, 0)),
	}), "the round attempted nothing, so the person's grant comes back")
	assert.Equal(t, 1, deployFixGrants([]tracker.Comment{
		grantMarker(time.Unix(1, 0)), grantFundedStart(time.Unix(2, 0)), outage(time.Unix(3, 0)), outage(time.Unix(4, 0)),
	}), "a repeated outage cannot mint a second grant")
	assert.Equal(t, 1, deployFixGrants([]tracker.Comment{
		grantMarker(time.Unix(1, 0)), budgetStart(time.Unix(2, 0)), outage(time.Unix(3, 0)),
	}), "an outaged BUDGET round never touches the grant — deployFixRounds is what refunds that one")
}

func TestDispatchDeployFixer_MarksOnlyTheGrantFundedRound(t *testing.T) {
	res := PRResult{URL: "https://example/pr/7", Number: 7}

	c1 := &fakeCommenter{}
	deps1 := newDeps(c1, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps1.dispatchDeployFixer(context.Background(), "SC-1", res, "feat/x", "CI failed", false, 1))
	require.Len(t, c1.added, 1)
	m1, ok := marker.ParseBody(c1.added[0])
	require.True(t, ok)
	_, has := m1.Fields[DeployFixGrantField]
	assert.False(t, has, "a round below the budget must not claim a grant funded it")
	assert.Equal(t, res.URL, m1.Fields["pr"])
	assert.Equal(t, "7", m1.Fields["number"])
	assert.Equal(t, "feat/x", m1.Fields["branch"])

	c2 := &fakeCommenter{}
	deps2 := newDeps(c2, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps2.dispatchDeployFixer(context.Background(), "SC-1", res, "feat/x", "CI failed", false, DefaultDeployFixRounds))
	require.Len(t, c2.added, 1)
	m2, ok := marker.ParseBody(c2.added[0])
	require.True(t, ok)
	assert.Equal(t, deployFixGrantValue, m2.Fields[DeployFixGrantField])
	assert.Equal(t, res.URL, m2.Fields["pr"])
	assert.Equal(t, "7", m2.Fields["number"])
	assert.Equal(t, "feat/x", m2.Fields["branch"])
}

// spentDoneFailedThread derives to (done, failed) with the budget spent — the
// shape both TestApplyTransition_MachineRetryMintsNoGrant and its positive
// control drive.
func spentDoneFailedThread() []tracker.Comment {
	return append(deployFixReadyComments(),
		budgetStart(time.Unix(3, 0)),
		budgetStart(time.Unix(5, 0)),
		cmt(DeployFailedHeader+"\nreason: CI checks failed on the pull request", time.Unix(7, 0)),
	)
}

func TestApplyTransition_MachineRetryMintsNoGrant(t *testing.T) {
	syncPRReview(t)
	thread := spentDoneFailedThread()
	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)
	require.True(t, isDeployRetry(BoardDoneStage, card), "sanity: the thread must derive to the retryable shape")

	req := BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage}

	// The daemon's own re-drive — redriveDeploy's shape — with an EMPTY Cause.
	c1 := &fakeCommenter{comments: append([]tracker.Comment{}, thread...)}
	deps1 := newDeps(c1, &fakeLauncher{}, &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}})
	_, err := deps1.ApplyRetryTransition(context.Background(), req)
	require.NoError(t, err)
	for _, b := range c1.added {
		assert.False(t, strings.HasPrefix(b, DeployRetryHeader), "the machine must not grant itself a round: %q", b)
	}

	// chainReviewWith's shape: ApplyTransition with a Cause set.
	for _, cause := range []WaitCause{WaitCauseChain, WaitCausePollBoundary} {
		c2 := &fakeCommenter{comments: append([]tracker.Comment{}, thread...)}
		deps2 := newDeps(c2, &fakeLauncher{}, &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}})
		causedReq := req
		causedReq.Cause = cause
		require.NoError(t, deps2.ApplyTransition(context.Background(), causedReq))
		for _, b := range c2.added {
			assert.False(t, strings.HasPrefix(b, DeployRetryHeader), "cause %q must not grant a round: %q", cause, b)
		}
	}

	// The positive control: a person's ApplyTransition with no Cause.
	c3 := &fakeCommenter{comments: append([]tracker.Comment{}, thread...)}
	deps3 := newDeps(c3, &fakeLauncher{}, &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}})
	require.NoError(t, deps3.ApplyTransition(context.Background(), req))
	var grantCount int
	for _, b := range c3.added {
		if strings.HasPrefix(b, DeployRetryHeader) {
			grantCount++
		}
	}
	assert.Equal(t, 1, grantCount, "a person's retry must post exactly one grant")
	assert.Equal(t, 1, deployFixGrants(c3.comments))
}

func TestApplyTransition_RetryWithBudgetLeftDoesNotSilentlySpendTheGrant(t *testing.T) {
	syncPRReview(t)
	syncDeploy(t)
	thread := append(deployFixReadyComments(),
		budgetStart(time.Unix(3, 0)),
		cmt(DeployFailedHeader+"\nreason: CI checks failed on the pull request", time.Unix(5, 0)))
	c := &fakeCommenter{comments: thread}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	deps := newDeps(c, &fakeLauncher{}, p)

	req := BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage}
	require.NoError(t, deps.ApplyTransition(context.Background(), req))

	var grantCount int
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployRetryHeader) {
			grantCount++
		}
	}
	require.Equal(t, 1, grantCount, "a person asked; the entitlement is recorded regardless of the budget's state")

	before := len(c.added)
	require.NoError(t, deployVia(t, deps, req))
	posted := c.added[before:]
	var started string
	for _, b := range posted {
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
	}
	require.NotEmpty(t, started, "the round the budget can still fund must dispatch")
	m, ok := marker.ParseBody(started)
	require.True(t, ok)
	_, has := m.Fields[DeployFixGrantField]
	assert.False(t, has, "a round the ticket's own budget paid for must not also claim the grant")
	assert.Equal(t, 1, deployFixGrants(c.comments), "the grant is banked for the round the budget cannot fund")
}

// A tracker that refuses the grant comment must not refuse the retry itself:
// grantDeployFixRound is best-effort, and the retry is worth running on the
// freshness rebase alone even if the grant could not be recorded.
func TestGrantDeployFixRound_CommenterErrorDoesNotFailTheRetry(t *testing.T) {
	syncPRReview(t)
	thread := spentDoneFailedThread()
	c := &fakeCommenter{comments: thread, addErr: errors.New("tracker unreachable"), addErrFor: DeployRetryHeader}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}})

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})
	require.NoError(t, err, "a failed grant comment must not fail the retry")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployRetryHeader), "the refused comment must not appear as posted")
	}
}
