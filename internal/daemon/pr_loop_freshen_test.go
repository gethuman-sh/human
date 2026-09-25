package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// countPosted counts posted comments starting with header.
func countPosted(c *fakeCommenter, header string) int {
	n := 0
	for _, b := range c.added {
		if strings.HasPrefix(b, header) {
			n++
		}
	}
	return n
}

func fixDoneThread() []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}
}

var fixDoneOutcome = PRLoopOutcome{ReviewRecorded: true, FixExit: PRFixDone, FixRecorded: true}

// The base advanced past the branch and merging it conflicts: the deploy
// fixer is dispatched BEFORE any reviewer runs, with the marker recording that
// it preceded the review, and no review round is spent on the drift (SC-5279).
func TestAdvancePRLoop_staleBaseConflict_dispatchesFixerBeforeReview(t *testing.T) {
	c := &fakeCommenter{comments: fixDoneThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{freshness: FreshnessConflict}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", fixDoneOutcome))

	assert.Equal(t, 1, p.freshened, "the base merge runs once before the launch")
	assert.Equal(t, 0, countPosted(c, PRReviewStartedHeader), "no review round starts on a conflicting branch")
	started, ok := posted(c, DeployFixStartedHeader)
	require.True(t, ok, "the deploy fixer is dispatched instead")
	m, parsed := marker.ParseBody(started)
	require.True(t, parsed)
	assert.Equal(t, deployFixBeforeReviewValue, m.Fields[DeployFixBeforeReviewField], "the marker records that the fixer preceded the review")
	assert.Equal(t, "feat/x", m.Fields["branch"])
	assert.Equal(t, "/human-deploy-fix SC-1 --pr=7 --branch=feat/x", l.prompt)
}

// The merge is textually clean but the fast tier is red on the integrated
// result: the deploy fixer is dispatched BEFORE any reviewer runs, exactly
// like a textual conflict, and no review round is spent on a candidate that
// does not build (SC-5279 acceptance criterion 1).
func TestAdvancePRLoop_staleBaseTestsFailed_dispatchesFixerBeforeReview(t *testing.T) {
	c := &fakeCommenter{comments: fixDoneThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{freshness: FreshnessTestsFailed}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", fixDoneOutcome))

	assert.Equal(t, 1, p.freshened, "the base merge runs once before the launch")
	assert.Equal(t, 0, countPosted(c, PRReviewStartedHeader), "no review round starts on a merge that fails the fast tier")
	started, ok := posted(c, DeployFixStartedHeader)
	require.True(t, ok, "the deploy fixer is dispatched instead")
	m, parsed := marker.ParseBody(started)
	require.True(t, parsed)
	assert.Equal(t, deployFixBeforeReviewValue, m.Fields[DeployFixBeforeReviewField], "the marker records that the fixer preceded the review")
	assert.Equal(t, "/human-deploy-fix SC-1 --pr=7 --branch=feat/x", l.prompt)
}

// A clean base merge moves the local branch and the reviewer reads the
// integrated candidate: the review round starts as before.
func TestAdvancePRLoop_staleBaseMerged_reviewsIntegratedBranch(t *testing.T) {
	c := &fakeCommenter{comments: fixDoneThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{freshness: FreshnessMerged}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", fixDoneOutcome))

	assert.Equal(t, 1, p.freshened)
	assert.Equal(t, 1, countPosted(c, PRReviewStartedHeader), "the review round starts on the merged branch")
	assert.Equal(t, 0, countPosted(c, DeployFixStartedHeader))
	assert.Equal(t, "/human-pr-review SC-1 --pr=7 --branch=feat/x", l.prompt)
}

// Git failing underneath the freshen is not a conflict and not a reason to
// stop the loop: the review runs on the branch as it is, and the CI gate's own
// freshness rebase still stands behind it.
func TestAdvancePRLoop_freshenError_stillReviews(t *testing.T) {
	c := &fakeCommenter{comments: fixDoneThread()}
	l := &fakeLauncher{}
	p := &fakeDeployer{freshenErr: errors.New("fetch: network down")}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", fixDoneOutcome))

	assert.Equal(t, 1, countPosted(c, PRReviewStartedHeader))
	assert.Equal(t, 0, countPosted(c, DeployFixStartedHeader))
	assert.Equal(t, "board-SC-1-prreview", l.name)
}

// A fixer the pre-review merge dispatched hands back to the reviewer on done:
// nothing has reviewed the integrated branch yet, so the CI gate and the merge
// are not next — the review is.
func TestAdvanceDeployFix_doneBeforeReview_launchesReviewerNotDeploy(t *testing.T) {
	syncDeploy(t)
	before := markerBody(marker.Marker{
		Type:   MarkerDeployFixStarted,
		Fields: fields("pr", "https://example/pr/7", "number", "7", "branch", "feat/x", DeployFixBeforeReviewField, deployFixBeforeReviewValue),
		Body:   "branch behind the base with a conflict — resolving it before the review",
	}, "pr", "number", "branch", DeployFixBeforeReviewField)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(before, time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitDone}))

	assert.Equal(t, 0, p.merged, "a pre-review fixer's done exit does not merge")
	assert.Equal(t, 1, p.publishCalls, "the fixer's resolution is still published")
	assert.Equal(t, 1, countPosted(c, PRReviewStartedHeader), "the reviewer is launched on the resolved branch")
	assert.Equal(t, "/human-pr-review SC-1 --pr=7 --branch=feat/x", l.prompt)
	_, failed := posted(c, DeployFailedHeader)
	assert.False(t, failed)
}

// The conflict is found before the FIRST review round: no reviewer has ever
// launched, so no pr-review-started marker exists to recover the PR binding
// from. The deploy-fix-started marker that dispatched this fixer already
// carries the real (number, url) — the handback must read it from there
// rather than default to PR #0 (SC-5279 follow-up to SC-5119).
func TestAdvanceDeployFix_doneBeforeFirstReview_recoversPRBindingFromDeployFixMarker(t *testing.T) {
	syncDeploy(t)
	before := markerBody(marker.Marker{
		Type:   MarkerDeployFixStarted,
		Fields: fields("pr", "https://example/pr/7", "number", "7", "branch", "feat/x", DeployFixBeforeReviewField, deployFixBeforeReviewValue),
		Body:   "branch behind the base with a conflict — resolving it before the review",
	}, "pr", "number", "branch", DeployFixBeforeReviewField)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(before, time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitDone}))

	assert.Equal(t, 1, countPosted(c, PRReviewStartedHeader), "the reviewer is launched on the resolved branch")
	assert.Equal(t, "/human-pr-review SC-1 --pr=7 --branch=feat/x", l.prompt, "PR #0 is never dispatched")
	_, failed := posted(c, DeployFailedHeader)
	assert.False(t, failed)
}

// The CI gate's fixer carries no before-review field, so its done exit keeps
// re-running the deploy — the record decides, not the fixer's exit alone.
func TestDispatchDeployFixer_fromCIGate_carriesNoBeforeReviewField(t *testing.T) {
	c := &fakeCommenter{comments: deployFixReadyComments()}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	require.NoError(t, deps.dispatchDeployFixer(context.Background(), "SC-1",
		PRResult{URL: "https://example/pr/7", Number: 7}, "feat/x", "CI failed", false))
	started, ok := posted(c, DeployFixStartedHeader)
	require.True(t, ok)
	m, parsed := marker.ParseBody(started)
	require.True(t, parsed)
	_, has := m.Fields[DeployFixBeforeReviewField]
	assert.False(t, has)
	assert.False(t, deployFixWasBeforeReview(append(c.comments, cmt(started, time.Unix(9, 0)))))
}
