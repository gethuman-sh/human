package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// shortCheckoutWait makes the interlock decide in milliseconds rather than the
// production fifteen minutes.
func shortCheckoutWait(t *testing.T) {
	t.Helper()
	interval, bound := checkoutFreeCheckInterval, DeployCheckoutWaitBound
	checkoutFreeCheckInterval, DeployCheckoutWaitBound = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { checkoutFreeCheckInterval, DeployCheckoutWaitBound = interval, bound })
}

// reviewedThread is the SC-5592 window: the inline review posted its verdict
// from inside the implementation container, which has not exited yet.
func reviewedThread() []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: autofix/sc-1\nreview: inline", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: pass", time.Unix(2, 0)),
	}
}

func TestRunDoneStage_WaitsWhileTheImplementationContainerHoldsTheCheckout(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Zero(t, p.call, "no pull request may be pushed into a checkout another stage holds")
	assert.Zero(t, l.calls, "no reviewer is launched")
	assert.Empty(t, c.added, "the interlock records nothing on the ticket")
}

func TestDeployRetry_WaitsWhileTheImplementationContainerHoldsTheCheckout(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	thread := append(reviewedThread(), cmt("[human:deploy-failed]\nreason: conflict", time.Unix(3, 0)))
	c := &fakeCommenter{comments: thread}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Zero(t, p.call, "no pull request may be pushed into a checkout another stage holds")
	assert.Zero(t, l.calls, "no reviewer is launched")
	assert.Empty(t, c.added, "the interlock records nothing on the ticket")
}

func TestRunDoneStage_ProceedsOnceTheContainerIsGone(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	calls := 0
	deps.LiveAgents = func() ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"board-SC-1-implementation"}, nil
		}
		return nil, nil
	}

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
	assert.Equal(t, "board-SC-1-prreview", l.name)
	assert.GreaterOrEqual(t, calls, 2, "this is what proves the deploy waits rather than refusing")
}

func TestRunDoneStage_VerificationContainerAlsoHoldsTheCheckout(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = liveAgents("board-SC-1-verification")

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Zero(t, p.call)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestRunDoneStage_NilListerDisablesTheInterlock(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = nil

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
	assert.Equal(t, "board-SC-1-prreview", l.name)
}

func TestRunDoneStage_UnreadableListingDoesNotBlockTheDeploy(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = func() ([]string, error) { return nil, errors.New("docker down") }

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
}

func TestRunDoneStage_DoneStageAgentsDoNotHoldTheCheckout(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = liveAgents("board-SC-1-prreview")

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
}
