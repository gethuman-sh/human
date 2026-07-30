package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// added reports whether any posted comment starts with header.
func posted(c *fakeCommenter, header string) (string, bool) {
	for _, b := range c.added {
		if strings.HasPrefix(b, header) {
			return b, true
		}
	}
	return "", false
}

// reviewStartedComments builds a thread with the loop's binding: a handoff (for
// card.Branch) plus n pr-review-started markers carrying PR number/url/branch.
func reviewStartedComments(n int, url string, number int, branch string) []tracker.Comment {
	cs := []tracker.Comment{cmt("[human:ready-for-review]\nbranch: "+branch, time.Unix(1, 0))}
	for i := 0; i < n; i++ {
		cs = append(cs, cmt(prReviewStartedBody(url, number, branch), time.Unix(int64(2+i), 0)))
	}
	return cs
}

func TestAdvancePRLoop_reviewApproved_merges(t *testing.T) {
	syncDeploy(t) // DeployBranch polls the CI gate; run it without real time.
	c := &fakeCommenter{comments: reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}, checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, FixExit: "", FixRecorded: true}))

	assert.Equal(t, 7, p.markedReady, "the approved PR must be un-drafted before merge")
	assert.Equal(t, 1, p.merged, "the un-drafted PR must merge")
	assert.Equal(t, "SC-1", closed)
	_, ok := posted(c, DeployedHeader)
	assert.True(t, ok, "a deployed marker must be posted after merge")
}

func TestAdvancePRLoop_changesRequested_launchesFixer(t *testing.T) {
	c := &fakeCommenter{comments: reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, FixExit: "", FixRecorded: true}))

	_, ok := posted(c, PRFixStartedHeader)
	assert.True(t, ok, "a pr-fix-started marker must be posted")
	assert.Equal(t, "board-SC-1-prfix", l.name)
	assert.Equal(t, "/human-pr-fix SC-1 --pr=7 --branch=feat/x", l.prompt)
}

func TestAdvancePRLoop_fixDone_launchesReviewer(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: "", ReviewRecorded: true, FixExit: PRFixDone, FixRecorded: true}))

	_, ok := posted(c, PRReviewStartedHeader)
	assert.True(t, ok, "a fresh pr-review-started marker must be posted")
	assert.Equal(t, "board-SC-1-prreview", l.name)
	assert.Equal(t, "/human-pr-review SC-1 --pr=7 --branch=feat/x", l.prompt)
}

// A done fix that left the reviewed head unchanged (no new commit) must red the
// card rather than re-review the same head forever — the SC-1760 convergence
// guard. It is a loud failure, not a human decision, so it reds the card rather
// than posting an options block.
func TestAdvancePRLoop_fixDoneUnchangedHead_escalates(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		FixExit: PRFixDone, FixRecorded: true, ReviewHead: "abc123", FixHead: "abc123",
	}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "an unchanged-head fix must escalate, not re-review")
	assert.Contains(t, failed, "added no commit")
	_, optionsPosted := posted(c, OptionsHeader)
	assert.False(t, optionsPosted, "a stalled done fix reds the card, it is not a decision fork")
	assert.Zero(t, l.calls, "escalation must launch no reviewer")
	assert.Zero(t, p.merged, "escalation must never merge")
}

func TestAdvancePRLoop_budgetExhausted_escalates(t *testing.T) {
	c := &fakeCommenter{comments: reviewStartedComments(DefaultPRReviewRounds, "https://example/pr/7", 7, "feat/x")}
	l := &fakeLauncher{}
	p := &fakeDeployer{}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, FixExit: "", FixRecorded: true}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "the budget-exhausted loop must escalate")
	assert.Contains(t, failed, "did not converge within the round budget")
	assert.Zero(t, l.calls, "escalation must launch no agent")
	assert.Zero(t, p.merged, "escalation must never merge")
}

func TestAdvancePRLoop_fixNeedsInput_escalates(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: "", ReviewRecorded: true, FixExit: ExitNeedsInput, FixRecorded: true}))

	// A fixer needs-input is a DECISION: it escalates to a [human:options] block,
	// not a red failed marker, so each direction is a clickable board choice.
	_, failedPosted := posted(c, PRReviewFailedHeader)
	assert.False(t, failedPosted, "needs-input must not red the card")
	block, ok := posted(c, OptionsHeader)
	require.True(t, ok, "needs-input must post an options block")
	stage, _, opts := parseOptionsBlock(block)
	assert.Equal(t, BoardImplementation, stage)
	require.GreaterOrEqual(t, len(opts), 1, "the fallback rebuild option keeps the block valid")
	assert.Contains(t, opts[0].Label, "Rebuild")
	assert.Zero(t, l.calls)
}

func TestAdvancePRLoop_fixNeedsInput_withOptions(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{
		FixExit:     ExitNeedsInput,
		FixRecorded: true,
		FixOptions:  []BoardOption{{ID: "1", Label: "A"}, {ID: "2", Label: "B"}},
		FixSummary:  "deferred X",
	}))

	block, ok := posted(c, OptionsHeader)
	require.True(t, ok)
	stage, ctx, opts := parseOptionsBlock(block)
	assert.Equal(t, BoardImplementation, stage)
	assert.Equal(t, "deferred X", ctx)
	require.Len(t, opts, 2)
	assert.Equal(t, "A", opts[0].Label)
	assert.Equal(t, "B", opts[1].Label)
	assert.Zero(t, l.calls)
}

func TestAdvancePRLoop_escalateIdempotent(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
		cmt(OptionsHeader+"\nstage: implementation\ncontext: prior\n1: Rebuild it", time.Unix(4, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: "", ReviewRecorded: true, FixExit: ExitNeedsInput, FixRecorded: true}))

	// A durable re-drive over a loop that already escalated must not double-post.
	assert.Empty(t, c.added, "an already-open options block short-circuits the escalation")
	assert.Zero(t, l.calls)
}

func TestAdvancePRLoop_unreadableVerdict_escalates(t *testing.T) {
	c := &fakeCommenter{comments: reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")}
	p := &fakeDeployer{}
	deps := newDeps(c, &fakeLauncher{}, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: "", ReviewRecorded: true, FixExit: "", FixRecorded: true}))

	_, ok := posted(c, PRReviewFailedHeader)
	assert.True(t, ok, "an unreadable verdict must escalate rather than merge")
	assert.Zero(t, p.merged)
	assert.Zero(t, p.markReadyCall)
}

func TestAdvancePRLoop_markReadyFails_deployFailed(t *testing.T) {
	c := &fakeCommenter{comments: reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")}
	p := &fakeDeployer{markReadyErr: errors.New("graphql: could not un-draft")}
	deps := newDeps(c, &fakeLauncher{}, p)

	err := deps.AdvancePRLoop(context.Background(), "SC-1", PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, FixExit: "", FixRecorded: true})
	require.Error(t, err)

	failed, ok := posted(c, DeployFailedHeader)
	require.True(t, ok, "a failed un-draft must red the card")
	assert.Contains(t, failed, "could not be marked ready for merge")
	assert.Zero(t, p.merged, "the merge must not be reached when the un-draft fails")
	assert.Zero(t, p.call, "DeployBranch must not push a PR when the un-draft fails")
}

func TestOpenDraftPRAndReview_opensDraftAndLaunches(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.openDraftPRAndReview(context.Background(), "SC-1", BoardCard{Branch: "feat/x"}))

	assert.True(t, p.req.Draft, "the loop's PR must open in draft state")
	started, ok := posted(c, PRReviewStartedHeader)
	require.True(t, ok, "a pr-review-started marker must be posted")
	assert.Contains(t, started, "number: 7")
	assert.Equal(t, "board-SC-1-prreview", l.name)
	assert.Equal(t, "/human-pr-review SC-1 --pr=7 --branch=feat/x", l.prompt)
}

func TestOpenDraftPRAndReview_alreadyMerged_shortCircuits(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	p := &fakeDeployer{alreadyMerged: true}
	deps := newDeps(c, l, p)
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }

	require.NoError(t, deps.openDraftPRAndReview(context.Background(), "SC-1", BoardCard{Branch: "feat/x"}))

	deployed, ok := posted(c, DeployedHeader)
	require.True(t, ok, "an already-merged branch ends deployed, not reviewed")
	assert.Contains(t, deployed, "already merged")
	assert.Equal(t, "SC-1", closed)
	assert.Zero(t, p.call, "no PR must be opened for already-merged work")
	assert.Zero(t, l.calls, "no reviewer must be launched for already-merged work")
}
