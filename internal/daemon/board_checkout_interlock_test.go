package daemon

import (
	"context"
	"errors"
	"strings"
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
	// SC-5878: the wait is recorded and then withdrawn — the board derives a card
	// from ticket markers alone, so a wait that recorded nothing rendered as a
	// bounce back to a finished review.
	require.Len(t, c.added, 2, "the queued deploy is recorded, and its abandonment at the bound")
	assert.True(t, strings.HasPrefix(c.added[0], DeployQueuedHeader))
	assert.True(t, strings.HasPrefix(c.added[1], DeployQueueAbandonedHeader))
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
	// The grant is posted BEFORE runDoneStage starts (SC-5595, AD1) — a person's
	// Retry deploy is recorded regardless of what the stage that follows does
	// with it, the same accepted trade-off as a retry the deploy cannot even
	// start (plan Risks: "a retry the deploy cannot even start banks a grant
	// anyway"). The interlock itself still pushes and launches nothing, but it
	// still records the queued wait and its abandonment (SC-5878).
	require.Len(t, c.added, 3, "the grant, the queued record and its abandonment")
	assert.True(t, strings.HasPrefix(c.added[0], DeployRetryHeader))
	assert.True(t, strings.HasPrefix(c.added[1], DeployQueuedHeader))
	assert.True(t, strings.HasPrefix(c.added[2], DeployQueueAbandonedHeader))
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
	// SC-5878: the wait was recorded, and — since the checkout freed before the
	// bound — never withdrawn; the loop's own started marker follows it.
	require.Len(t, c.added, 2)
	assert.True(t, strings.HasPrefix(c.added[0], DeployQueuedHeader))
	assert.True(t, strings.HasPrefix(c.added[1], PRReviewStartedHeader))
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
	require.Len(t, c.added, 2, "the queued deploy is recorded, and its abandonment at the bound")
	assert.True(t, strings.HasPrefix(c.added[0], DeployQueuedHeader))
	assert.True(t, strings.HasPrefix(c.added[1], DeployQueueAbandonedHeader))
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

// SC-5878: the board draws a card from ticket markers alone, so a deploy that is
// accepted and then held for minutes must say so on the ticket. Without the
// record the card re-derives to (verification, done) — the person's drop appears
// to bounce — and a re-drop is not a duplicate, so it queues a second deploy.
func TestRunDoneStage_QueuedDeployIsRecordedOnTheTicket(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)

	require.NotEmpty(t, c.added, "a deploy queued behind the container must be recorded on the ticket")
	queued := append(reviewedThread(), cmt(c.added[0], time.Unix(3, 0)))
	card := DeriveBoardCard(queued, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage, "the card must move to Deploy, not read as a finished review")
	assert.Equal(t, BoardRunning, card.State)
	assert.True(t, isDuplicateDrop(BoardDoneStage, card), "a second drop must be refused as a duplicate")
}

// The other half of the same rule: giving up at the bound must say so, and the
// card must then read as it did before the gesture rather than as a done stage
// nothing is running.
func TestRunDoneStage_AbandonedQueuedDeploySaysWhyAndRestoresTheCard(t *testing.T) {
	syncPRReview(t)
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "https://example/pr/7"}}
	deps := newDeps(c, l, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)

	require.Len(t, c.added, 2, "the queued deploy and its abandonment are both recorded")
	thread := append(reviewedThread(),
		cmt(c.added[0], time.Unix(3, 0)),
		cmt(c.added[1], time.Unix(4, 0)))
	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardVerification, card.Stage, "an abandoned queue returns the card to where the gesture found it")
	assert.Equal(t, BoardDone, card.State)
	assert.NotEmpty(t, card.DeployQueueAbandoned, "the card must say why it came back")
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
