package daemon

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// approvedLoopThread is a thread whose reviewer just approved: the live hook
// path and the reconcile pass both read it in this state.
func approvedLoopThread() []tracker.Comment {
	base := time.Now().Add(-time.Minute)
	return []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "2", Created: base.Add(time.Second)},
	}
}

// lockedCommenter serializes the fake so two drives can share it.
type lockedCommenter struct {
	mu sync.Mutex
	fakeCommenter
}

func (l *lockedCommenter) ListComments(ctx context.Context, key string) ([]tracker.Comment, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeCommenter.ListComments(ctx, key)
}

func (l *lockedCommenter) AddComment(ctx context.Context, key, body string) (*tracker.Comment, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeCommenter.AddComment(ctx, key, body)
}

// gatedDeployer holds the first drive inside the engine until the test says
// so, which is what makes the second drive provably CONCURRENT with it rather
// than merely after it.
type gatedDeployer struct {
	*fakeDeployer
	entered chan struct{}
	release chan struct{}
}

func (g *gatedDeployer) PushAndCreatePR(ctx context.Context, req PRRequest) (PRResult, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.fakeDeployer.PushAndCreatePR(ctx, req)
}

// One approval reached two drivers within a minute — the reviewer's Stop on
// the live path and the reconcile pass on its timer — and each ran the merge.
// The second drive of a ticket already being driven does nothing (SC-5089).
func TestAdvancePRLoop_concurrentDrivesMergeOnce(t *testing.T) {
	c := &lockedCommenter{fakeCommenter: fakeCommenter{comments: approvedLoopThread()}}
	p := &gatedDeployer{
		fakeDeployer: &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, checks: []forge.ChecksState{forge.ChecksPassing}, mergeable: true},
		entered:      make(chan struct{}, 1),
		release:      make(chan struct{}),
	}
	deps := newDeps(nil, &fakeLauncher{}, nil)
	deps.Commenter, deps.Deployer = c, p
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true}

	first := make(chan error, 1)
	go func() { first <- deps.AdvancePRLoop(context.Background(), "SC-1", outcome) }()
	<-p.entered // the first drive holds the ticket and is inside the engine
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", outcome), "the second drive stands down without error")
	close(p.release)
	require.NoError(t, <-first)

	assert.Equal(t, 1, p.merged, "one approval is one merge")
	assert.Equal(t, 1, p.markReadyCall, "the draft is released once")
	passed := 0
	for _, body := range c.added {
		if strings.HasPrefix(body, PRReviewPassedHeader) {
			passed++
		}
	}
	assert.Equal(t, 1, passed, "the convergence is recorded once")
	_, drivesHeld := prLoopDrives.Load("SC-1")
	assert.False(t, drivesHeld, "the drive is released when it ends")
}

// A drive that ends releases the ticket for the next one: the loop is driven
// once per event, not once per ticket lifetime.
func TestAdvancePRLoop_nextDriveRunsAfterTheFirstEnds(t *testing.T) {
	c := &fakeCommenter{comments: approvedLoopThread()}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, checks: []forge.ChecksState{forge.ChecksPassing}, mergeable: true}
	deps := newDeps(c, &fakeLauncher{}, p)
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true}

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", outcome))
	p.alreadyMerged = true
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", outcome))

	assert.Equal(t, 1, p.merged)
}

// The forge refuses to un-draft a pull request that is already merged. That
// refusal is not a deploy failure: the work shipped, and the engine's carve-out
// records it as deployed rather than redding a finished card (SC-5089, LOC-3).
func TestAdvancePRLoop_mergedPRIsReportedMergedNotFailed(t *testing.T) {
	c := &fakeCommenter{comments: approvedLoopThread()}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, alreadyMerged: true, markReadyErr: assert.AnError}
	deps := newDeps(c, &fakeLauncher{}, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true}))

	_, deployed := posted(c, DeployedHeader)
	assert.True(t, deployed)
	_, failed := posted(c, DeployFailedHeader)
	assert.False(t, failed, "a merged PR that cannot be un-drafted is merged, not failed")
	assert.Zero(t, p.markReadyCall, "nothing to un-draft on merged work")
}

// A loop started by `human deploy --branch` has no ready-for-review handoff;
// its branch lives on its own pr-review-started marker. The merge step and the
// approval marker must use it, or the engine pushes an empty branch (SC-5119).
func TestAdvancePRLoop_cliStartedLoopMergesTheStartMarkersBranch(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	base := time.Now().Add(-time.Minute)
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:deploy-started]\nbranch: feat/x", ID: "1", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "2", Created: base.Add(time.Second)},
	}}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, checks: []forge.ChecksState{forge.ChecksPassing}, mergeable: true}
	deps := newDeps(c, &fakeLauncher{}, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewHead: head}))

	assert.Equal(t, "feat/x", p.req.Branch, "the engine must ship the loop's branch, not an empty handoff branch")
	assert.Equal(t, 1, p.merged)
	passed, ok := posted(c, PRReviewPassedHeader)
	require.True(t, ok)
	assert.Contains(t, passed, "branch: feat/x")
	got, bound := currentApproval(c.comments, "feat/x")
	assert.True(t, bound, "a re-run deploy must be able to reuse this approval")
	assert.Equal(t, head, got)
}

// The fixer dispatch carries the same branch, so a CLI-started loop's fixer
// works the reviewed branch rather than an empty one.
func TestAdvancePRLoop_cliStartedLoopDispatchesTheFixerOnTheBranch(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true}))

	assert.Equal(t, "/human-pr-fix SC-1 --pr=7 --branch=feat/x", l.prompt)
}

// doneStageBranch prefers the start marker's own branch, but a marker posted
// before this fix (or a reconcile pass reading an older thread) may carry
// none — the handoff-driven, board-started case both existing loops otherwise
// exercise only through threads where the two sources agree. The fallback must
// still resolve to the handoff branch rather than an empty string.
func TestDoneStageBranch_fallsBackToHandoffWhenStartMarkerCarriesNone(t *testing.T) {
	comments := []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1", Created: time.Unix(1, 0)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7", ID: "2", Created: time.Unix(2, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "feat/x", doneStageBranch(comments, card))
}

// The ordinary case: the start marker's own branch wins even when it differs
// from the handoff, which is the whole point of reading it (SC-5119).
func TestDoneStageBranch_prefersStartMarkerOverHandoff(t *testing.T) {
	comments := []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/old", ID: "1", Created: time.Unix(1, 0)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/new", ID: "2", Created: time.Unix(2, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "feat/new", doneStageBranch(comments, card))
}

// A stale start marker from an EARLIER round must never outrank a newer
// handoff: a ticket that reached the done stage once (pr-review-started names
// feat/a), then went back through implementation and handed off a different
// branch (feat/b), resolves to feat/b — not the source-precedence winner from
// a round the ticket has since moved past. Trusting feat/a here would deploy
// stale work and, if feat/a happens to already be on the base, silently record
// the NEW work as shipped via the already-merged carve-out (SC-5396).
func TestDoneStageBranch_newerHandoffOutranksAStaleStartMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/a", ID: "1", Created: time.Unix(1, 0)},
		{Body: "[human:ready-for-review]\nbranch: feat/b", ID: "2", Created: time.Unix(2, 0)},
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "feat/b", doneStageBranch(comments, card))
}

// A re-drive from the reconcile pass carries no exit event and evidence older
// than the pass. When the thread names a step with no record and that step's
// agent is alive, the step is running, not unreadable: the re-drive stands
// down instead of redding a card over a live fixer (SC-5120).
func TestAdvancePRLoop_redriveStandsDownWhileTheStepsAgentIsAlive(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	thread := []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "2", Created: base.Add(time.Second)},
		{Body: "[human:pr-fix-started]", ID: "3", Created: base.Add(2 * time.Second)},
	}
	for _, tc := range []struct {
		name       string
		aliveNames []string
		agent      string
		fixStale   bool
		escalates  bool
		asked      []string
	}{
		{"re-drive, fixer alive", []string{"board-SC-1-prfix"}, "", false, false, []string{"board-SC-1-prfix"}},
		{"re-drive, fixer gone", nil, "", false, true, []string{"board-SC-1-prfix", "board-SC-1-deployfix"}},
		{"the fixer's own exit event", []string{"board-SC-1-prfix"}, "board-SC-1-prfix", false, true, nil},
		// A prior round's FixRecorded/FixStale leftover must not read as THIS
		// round's step already having reported in: stepRecorded alone stays
		// true forever once round 1 writes it, so from round 2 on only the
		// staleness check tells a live fixer apart from a finished one (SC-5120).
		{"re-drive, prior round's stale fix record, fixer alive", []string{"board-SC-1-prfix"}, "", true, false, []string{"board-SC-1-prfix"}},
		// SC-5591: the done stage's third agent. No loop marker names it, so the
		// half the thread names is gone while the card is still owned.
		{"re-drive, only the deploy fixer alive", []string{"board-SC-1-deployfix"}, "", false, false, []string{"board-SC-1-prfix", "board-SC-1-deployfix"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCommenter{comments: thread}
			var asked []string
			deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
			deps.LoopStepAlive = func(name string) bool {
				asked = append(asked, name)
				return slices.Contains(tc.aliveNames, name)
			}

			require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
				PRLoopOutcome{
					ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, Agent: tc.agent,
					FixRecorded: tc.fixStale, FixStale: tc.fixStale,
				}))

			_, failed := posted(c, PRReviewFailedHeader)
			assert.Equal(t, tc.escalates, failed)
			assert.Equal(t, tc.asked, asked)
		})
	}
}

// SC-5591, the drive side: the merge that dispatched the deploy fixer runs
// inside the APPROVED review's own drive, so the thread's newest loop marker
// still names the review and its recorded verdict satisfies stepRecorded. A
// re-drive therefore skipped the liveness probe entirely and went straight back
// to PRActionMerge — against the branch the fixer was mid-rebase on.
func TestAdvancePRLoop_redriveDoesNotMergeOverALiveDeployFixer(t *testing.T) {
	c := &fakeCommenter{comments: approvedLoopThread()}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, checks: []forge.ChecksState{forge.ChecksPassing}, mergeable: true}
	var asked []string
	deps := newDeps(c, &fakeLauncher{}, p)
	deps.LoopStepAlive = func(name string) bool {
		asked = append(asked, name)
		return name == agentNameFor("SC-1", deployFixAgentStage)
	}

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true}))

	assert.Zero(t, p.merged, "the fixer owns the branch; a second merge races its rebase")
	assert.Zero(t, p.markReadyCall)
	_, passed := posted(c, PRReviewPassedHeader)
	assert.False(t, passed, "nothing converged — the drive stood down")
	_, failed := posted(c, DeployFailedHeader)
	assert.False(t, failed, "and nothing failed")
	assert.Equal(t, []string{"board-SC-1-deployfix"}, asked,
		"an approved review's record short-circuits the half probe, so the deployfix probe must sit outside it")
}
