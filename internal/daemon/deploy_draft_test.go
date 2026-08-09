package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// draftDeployDeps wires a deploy whose PR is adopted in the forge's draft state —
// what every deploy that did not come through the review loop's own approval
// finds, because the loop opens its PR draft and only approval un-drafts it.
func draftDeployDeps(t *testing.T, mergeDraft bool) (*fakeCommenter, *fakeDeployer, BoardTransitionDeps) {
	t.Helper()
	c := &fakeCommenter{}
	d := &fakeDeployer{
		res:    PRResult{URL: "https://example/pr/7", Number: 7, Draft: true},
		checks: []forge.ChecksState{forge.ChecksPassing},
	}
	deps := newDeps(c, &fakeLauncher{}, d)
	deps.MergeDraftPR = mergeDraft
	return c, d, deps
}

// SC-4027. The forge reports a conflict-free draft as mergeable, so nothing
// upstream catches it and the gate used to spend the whole CI wait and then take
// a 405 that named nothing about drafts. Decide it before the CI gate, and say
// what is actually holding the change.
func TestDeployBranch_DraftPRIsRefusedWithItsRealReason(t *testing.T) {
	c, d, deps := draftDeployDeps(t, false)

	err := deps.DeployBranch(context.Background(), "SC-1", "SC-1", "body", "autofix/sc-1")

	require.Error(t, err)
	assert.Zero(t, d.merged, "a draft must not reach the merge")
	assert.Zero(t, d.checkCall, "nor spend the CI gate first")
	assert.Zero(t, d.markReadyCall, "and must not be un-drafted without being asked")

	body, ok := posted(c, DeployFailedHeader)
	require.True(t, ok, "the card must say why")
	assert.Contains(t, body, "held in draft by the machine review loop")
	assert.Contains(t, body, "--ready")
}

// The person's gesture: --ready means "I have judged this reviewed enough". It
// un-drafts and ships, which is the whole point of having a gesture at all.
func TestDeployBranch_ReadyOverrideUnDraftsAndMerges(t *testing.T) {
	_, d, deps := draftDeployDeps(t, true)

	require.NoError(t, deps.DeployBranch(context.Background(), "SC-1", "SC-1", "body", "autofix/sc-1"))

	assert.Equal(t, 7, d.markedReady, "the held PR is released deliberately")
	assert.Equal(t, 1, d.merged)
}

// A non-draft PR is untouched: the change adds a stop, not a step.
func TestDeployBranch_NonDraftPRIsUnaffected(t *testing.T) {
	c := &fakeCommenter{}
	d := &fakeDeployer{
		res:    PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksPassing},
	}
	deps := newDeps(c, &fakeLauncher{}, d)

	require.NoError(t, deps.DeployBranch(context.Background(), "SC-1", "SC-1", "body", "autofix/sc-1"))

	assert.Zero(t, d.markReadyCall, "nothing to un-draft")
	assert.Equal(t, 1, d.merged)
}

// The loop's own approval path is the one caller that legitimately ships a PR
// that WAS draft: it un-drafts first, so by the time the engine re-adopts it the
// PR is ready and the new refusal never fires.
func TestAdvancePRLoop_ApprovedStillMergesThroughTheDraftCheck(t *testing.T) {
	c := &fakeCommenter{comments: reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")}
	c.comments = append(c.comments,
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)))
	d := &fakeDeployer{
		res:    PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksPassing},
	}
	deps := newDeps(c, &fakeLauncher{}, d)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, FixRecorded: true,
	}))

	assert.Equal(t, 7, d.markedReady, "the loop un-drafts on approval, as before")
	assert.Equal(t, 1, d.merged)
}

// isTransientMergeRefusal matched any 405, and a draft refusal is a 405 — so the
// merge was retried for the full window on a refusal that could never lift.
func TestIsTransientMergeRefusal_DraftIsTerminal(t *testing.T) {
	draft := errForTest("github PUT /repos/o/r/pulls/450/merge returned 405: " +
		`{"message":"Pull Request is still a draft"}`)
	stale := errForTest("github PUT /repos/o/r/pulls/450/merge returned 405: " +
		`{"message":"Pull Request is not mergeable"}`)

	assert.False(t, isTransientMergeRefusal(draft), "nothing about the branch changes while a draft is retried")
	assert.True(t, isTransientMergeRefusal(stale), "the racy post-rebase refusal still rides out")
}

type errForTest string

func (e errForTest) Error() string { return string(e) }

var _ tracker.Commenter = (*fakeCommenter)(nil)
