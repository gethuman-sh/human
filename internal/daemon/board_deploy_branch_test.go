package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// SC-5396 (b): `human deploy --branch` records the branch on
// [human:deploy-started] and the fixer's dispatch on [human:deploy-fix-started].
// The resolver read the handoff alone, so a CLI deploy resolved "".
func TestDoneStageBranch_readsTheDeployStartedMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Body: DeployStartedHeader + "\nbranch: feat/x", ID: "1", Created: time.Unix(1, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	require.Empty(t, card.Branch, "no handoff — the card itself carries no branch")
	assert.Equal(t, "feat/x", doneStageBranch(comments, card))
}

func TestDoneStageBranch_readsTheDeployFixStartedMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Body: DeployFixStartedHeader + "\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: time.Unix(1, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "feat/x", doneStageBranch(comments, card))
}

func TestDoneStageBranch_prefersTheLoopStartMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Body: DeployStartedHeader + "\nbranch: feat/old", ID: "1", Created: time.Unix(1, 0)},
		{Body: PRReviewStartedHeader + "\npr: u\nnumber: 7\nbranch: feat/new", ID: "2", Created: time.Unix(2, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "feat/new", doneStageBranch(comments, card))
}

func TestDoneStageBranch_emptyWhenNoMarkerNamesOne(t *testing.T) {
	comments := []tracker.Comment{
		{Body: DeployFailedHeader + "\nboom", ID: "1", Created: time.Unix(1, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "", doneStageBranch(comments, card))
}

// SC-5396 (c): a deploy started from the command line with an explicit branch
// and no review handoff must be retryable by the machine. Before the fix
// ApplyTransition(done->done) refused it with
// "no branch recorded on ready-for-review handoff" about a branch written
// plainly on the deploy's own start marker.
func TestApplyTransitionDeployRetry_findsTheBranchOnTheDeployStartedMarker(t *testing.T) {
	syncPRReview(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(DeployStartedHeader+"\nbranch: feat/x", time.Unix(1, 0)),
		cmt(DeployFailedHeader+"\nthe forge refused the merge", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 11, URL: "https://example/pr/11"}}
	deps := newDeps(c, l, p)

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Equal(t, 1, p.call, "the draft PR must be opened for the recorded branch")
	assert.Equal(t, "feat/x", p.req.Branch)
	assert.Equal(t, "board-SC-1-prreview", l.name)
	for _, b := range c.added {
		assert.NotContains(t, b, "no branch recorded", "the refusal must not fire on a branch the deploy recorded")
	}
}

// The refusal survives for a card that genuinely has no branch anywhere
// (SC-297: nothing to ship), with a reason that no longer blames the handoff.
func TestApplyTransitionDeployRetry_refusesWhenNoMarkerNamesABranch(t *testing.T) {
	syncPRReview(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(DeployStartedHeader, time.Unix(1, 0)),
		cmt(DeployFailedHeader+"\nboom", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{}
	deps := newDeps(c, l, p)

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})

	require.Error(t, err)
	assert.Zero(t, p.call)
	var found bool
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			found = true
			assert.Contains(t, b, "no branch recorded on this ticket")
		}
	}
	assert.True(t, found, "a DeployFailedHeader body naming the refusal must be posted")
}
