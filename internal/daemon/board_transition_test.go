package daemon

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	humanerrors "github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// fakeCommenter records AddComment bodies and returns canned ListComments. It
// assigns each posted comment a monotonic numeric id, mirroring the
// server-assigned, server-ordered ids every real backend returns — the claim
// gate's "lowest comment id wins" arbitration reads them.
type fakeCommenter struct {
	comments []tracker.Comment
	added    []string
	addErr   error
	nextID   int
}

func (f *fakeCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	return f.comments, nil
}

func (f *fakeCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.added = append(f.added, body)
	f.nextID++
	c := tracker.Comment{ID: strconv.Itoa(f.nextID), Body: body, Created: time.Now()}
	f.comments = append(f.comments, c)
	return &c, nil
}

// fakeLauncher records the launch name/prompt and can fail.
type fakeLauncher struct {
	name   string
	prompt string
	err    error
	calls  int
}

func (f *fakeLauncher) Launch(_ context.Context, name, prompt, _, _ string) error {
	f.calls++
	f.name = name
	f.prompt = prompt
	return f.err
}

// fakeDeployer scripts the deploy pipeline steps: PR creation, successive
// checks poll results, merge, and branch deletion.
type fakeDeployer struct {
	prErr     error
	res       PRResult
	req       PRRequest
	call      int
	checks    []forge.ChecksState
	checksErr error
	checkCall int
	mergeErr  error
	merged    int
	deleted   []string
	// ensureErr is returned by EnsureMergeable — a non-nil value models a branch
	// that could not be made current with main (a real rebase conflict).
	ensureErr error
	// ensured counts EnsureMergeable calls so a test can assert the freshness
	// stage ran exactly once before the merge.
	ensured int
	// rebased is EnsureMergeable's report that it rewrote and re-pushed the
	// branch — the deploy must then wait out the forge's mergeability recompute.
	rebased bool
	// mergeUntil models GitHub's 405 on a stale branch: MergePullRequest fails
	// with a merge-conflict error until EnsureMergeable has run, then succeeds.
	mergeUntil bool
	// checksPassed counts how many times PullRequestChecks settled on Passing —
	// the pre-rebase CI gate is one, the post-rebase re-gate the second (SC-1184).
	checksPassed int
	// mergeBlockedUntilRegate models the SC-1184 race: the freshness rebase's
	// re-push triggers fresh CI on the new head, and the forge 405s the merge
	// ("Pull Request is not mergeable", state unstable) until that fresh CI has
	// been re-gated. MergePullRequest returns the transient 405 until
	// PullRequestChecks has settled on Passing a second time.
	mergeBlockedUntilRegate bool
	// mergeTransientUntil models a purely transient 405: the forge refuses the
	// merge ("not mergeable") for this many attempts, then accepts it — it
	// exercises the bounded-backoff merge retry independent of the CI re-gate.
	mergeTransientUntil int
	// mergeable is the forge's own end-state merge verdict reported by
	// PullRequestMergeable — the fallback signal when the mechanical rebase in
	// EnsureMergeable conflicts on an intermediate commit (SC-804).
	mergeable    bool
	mergeableErr error
	// mergeableAfter models the forge's asynchronous mergeability recompute
	// after a re-push: PullRequestMergeable reports false until it has been
	// polled this many times, then true. Zero disables (verdict = mergeable).
	mergeableAfter int
	mergeableCalls int
	// alreadyMerged models a branch whose work is already on the base: the deploy
	// must short-circuit to a clean no-op instead of opening a doomed PR (SC-911).
	alreadyMerged bool
	// markedReady captures the PR number the review loop un-drafted on approval;
	// markReadyErr models a forge that refuses the un-draft.
	markedReady   int
	markReadyErr  error
	markReadyCall int
	// published records the branches whose deploy-fixer resolution was carried to
	// origin, and publishErr models a publish the host refused (a source behind
	// origin) — the fixer's work is local, so an unpublished resolution is
	// invisible to the deploy that follows.
	published    []string
	publishErr   error
	publishCalls int
	// prState is the full PR read surface ReadPullRequest returns — the checks
	// the failure/timeout headlines name the offending entries from; prStateErr
	// models a read failure that must degrade the headline to its bare reason.
	prState    *forge.PullRequestState
	prStateErr error
}

func (f *fakeDeployer) ReadPullRequest(_ context.Context, _ string, _ int) (*forge.PullRequestState, error) {
	if f.prStateErr != nil {
		return nil, f.prStateErr
	}
	return f.prState, nil
}

func (f *fakeDeployer) PublishResolvedBranch(_ context.Context, _, branch string) (bool, error) {
	f.publishCalls++
	if f.publishErr != nil {
		return false, f.publishErr
	}
	f.published = append(f.published, branch)
	return true, nil
}

func (f *fakeDeployer) PushAndCreatePR(_ context.Context, req PRRequest) (PRResult, error) {
	f.call++
	f.req = req
	if f.prErr != nil {
		return PRResult{}, f.prErr
	}
	return f.res, nil
}

func (f *fakeDeployer) PullRequestChecks(_ context.Context, _ string, _ int) (forge.ChecksState, error) {
	if f.checksErr != nil {
		return "", f.checksErr
	}
	i := f.checkCall
	if i >= len(f.checks) {
		i = len(f.checks) - 1
	}
	f.checkCall++
	state := f.checks[i]
	if state == forge.ChecksPassing {
		f.checksPassed++
	}
	return state, nil
}

func (f *fakeDeployer) EnsureMergeable(_ context.Context, _ PRRequest) (bool, error) {
	f.ensured++
	return f.rebased, f.ensureErr
}

func (f *fakeDeployer) PullRequestMergeable(_ context.Context, _ string, _ int) (bool, error) {
	f.mergeableCalls++
	if f.mergeableErr != nil {
		return false, f.mergeableErr
	}
	if f.mergeableAfter > 0 {
		return f.mergeableCalls >= f.mergeableAfter, nil
	}
	return f.mergeable, nil
}

func (f *fakeDeployer) MergePullRequest(_ context.Context, _ string, _ int) error {
	f.merged++
	// A stale branch mirrors GitHub's 405 "merge conflicts" until the freshness
	// stage (EnsureMergeable) has rebased and re-pushed it.
	if f.mergeUntil && f.ensured == 0 {
		return errors.New("Pull Request has merge conflicts")
	}
	// The freshness rebase re-triggered CI on the new head; the forge 405s the
	// merge until that fresh CI has been re-gated to Passing a second time.
	if f.mergeBlockedUntilRegate && f.checksPassed < 2 {
		return errors.New(`405 Pull Request is not mergeable`)
	}
	// A purely transient racy refusal: the forge reports the head not-mergeable
	// for a beat after the re-push, then accepts the merge.
	if f.merged <= f.mergeTransientUntil {
		return errors.New(`405 Pull Request is not mergeable`)
	}
	return f.mergeErr
}

func (f *fakeDeployer) DeleteRemoteBranch(_ context.Context, _, branch string) error {
	f.deleted = append(f.deleted, branch)
	return nil
}

func (f *fakeDeployer) BranchMerged(_ context.Context, _, _ string) bool {
	return f.alreadyMerged
}

func (f *fakeDeployer) MarkReadyForReview(_ context.Context, _ string, number int) error {
	f.markReadyCall++
	if f.markReadyErr != nil {
		return f.markReadyErr
	}
	f.markedReady = number
	return nil
}

func newDeps(c *fakeCommenter, l *fakeLauncher, p *fakeDeployer) BoardTransitionDeps {
	return BoardTransitionDeps{Commenter: c, Launcher: l, Deployer: p, WorkspaceDir: "/ws", ConfigDir: "/ws"}
}

// syncDeploy makes the deploy pipeline run inline (and poll without real
// time) so tests observe its markers deterministically.
func syncDeploy(t *testing.T) {
	t.Helper()
	origStart, origInterval := startDeploy, deployCheckInterval
	startDeploy = func(d BoardTransitionDeps, req BoardTransitionRequest, card BoardCard) {
		d.deploy(context.Background(), req, card)
	}
	deployCheckInterval = time.Millisecond
	t.Cleanup(func() { startDeploy, deployCheckInterval = origStart, origInterval })
}

// syncPRReview makes the review-loop's first phase (open draft PR, launch
// reviewer) run inline so tests observe its markers deterministically. Mirrors
// syncDeploy, which overrides startDeploy.
func syncPRReview(t *testing.T) {
	t.Helper()
	orig := startPRReview
	startPRReview = func(d BoardTransitionDeps, req BoardTransitionRequest, card BoardCard) {
		_ = d.openDraftPRAndReview(context.Background(), req.PMKey, card)
	}
	t.Cleanup(func() { startPRReview = orig })
}

// deployVia drives the deploy engine synchronously the way the review loop's
// approval action reaches it (MarkReadyForReview + DeployBranch). Since the
// review→fix loop now fronts runDoneStage, the deploy-pipeline tests exercise
// DeployBranch directly rather than through the transition entry point, which
// only opens the draft PR and launches the reviewer.
func deployVia(t *testing.T, deps BoardTransitionDeps, req BoardTransitionRequest) error {
	t.Helper()
	comments, err := deps.Commenter.ListComments(context.Background(), req.PMKey)
	require.NoError(t, err)
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	return deps.DeployBranch(context.Background(), req.PMKey, req.PMTitle, doneBody(req.PMKey, card), card.Branch)
}

func TestApplyTransitionBackwardRejected(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardBacklog})
	require.Error(t, err)
	assert.Empty(t, c.added)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionSkipRejected(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	// Backlog -> Implementation skips Planning.
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionGatedBlock(t *testing.T) {
	// Planning running (not done) must block advancing to Implementation.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionRetriesFailedPlanning(t *testing.T) {
	// The "Retry plan" gesture targets planning while the card already derives
	// to planning/failed — the forward-only rule alone rejects that, leaving
	// the gesture dead (SC-355). A failed planning card must relaunch planning.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:planning-started]", time.Unix(1, 0)),
		cmt("[human:planning-failed]\nagent exited without completing the stage", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardPlanning})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-ticket-review SC-1")
	assert.Contains(t, l.prompt, "/human-plan SC-1")
	assert.Equal(t, "board-SC-1-planning", l.name)
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
}

func TestApplyTransitionRetriesFailedBuild(t *testing.T) {
	// A failed implementation card was a dead end: Retry fix is bug-pane-only,
	// Retry plan is planning-only, and every drop rejects it (SC-591). The
	// "Retry build" gesture targets implementation while the card derives to
	// implementation/failed — it must relaunch the executor, plan intact.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:implementation-started]", time.Unix(2, 0)),
		cmt("[human:implementation-failed]\nagent exited without completing the stage", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-execute SC-1")
	assert.Contains(t, l.prompt, "BOARD CONTEXT", "headless dispatch must carry the no-push, no-questions rules")
	assert.Equal(t, "board-SC-1-implementation", l.name)
	// The prior stage (plan-ready) finished at Unix(1,0), decades before this
	// retry — the retry's cause (WaitCauseRetry) is over StageWaitThreshold, so
	// it is attributed with a [human:stage-wait] record ahead of the started
	// marker (SC-2462).
	require.Len(t, c.added, 2)
	assert.True(t, strings.HasPrefix(c.added[0], StageWaitHeader))
	assert.Equal(t, ImplementationStartedHeader, c.added[1])
}

func TestApplyTransitionRunningBuildNotRelaunched(t *testing.T) {
	// Contract pin: build retry is for FAILED runs only — a running build hits
	// the idempotency guard and must not spawn a second agent.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplyTransitionRunningPlanningNotRelaunched(t *testing.T) {
	// Contract pin: retry is for FAILED planning only — a running planning
	// card hits the idempotency guard and must not spawn a second agent.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardPlanning})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplyTransitionBacklogToPlanning(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-ticket-review SC-1")
	assert.Contains(t, l.prompt, "/human-plan SC-1")
	assert.Equal(t, "board-SC-1-planning", l.name)
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
}

func TestApplyTransitionPlanningToImplementation(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-execute HUM-9")
	assert.Contains(t, l.prompt, "BOARD CONTEXT", "headless dispatch must carry the no-push, no-questions rules")
	assert.Contains(t, c.added, ImplementationStartedHeader)
}

func TestApplyTransitionImplementationToVerification(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})
	require.NoError(t, err)
	// SC-695: the review dispatch must pin the reviewer to the handoff branch,
	// not leave it to free-associate from whatever HEAD the worktree sits on.
	assert.Equal(t, "/human-review HUM-9 --branch=feat/x", l.prompt)
	assert.Contains(t, c.added, ReviewStartedHeader)
}

func TestApplyTransitionReviewDispatchCarriesBranchBinding(t *testing.T) {
	// SC-695: a full handoff (branch + commits) must thread both into the
	// review prompt as an authoritative binding — the reviewer verifies the
	// checked-out code IS this branch and these commits before reviewing.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x\ncommits: abc123, def456", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})
	require.NoError(t, err)
	assert.Equal(t, "/human-review HUM-9 --branch=feat/x --commits=abc123, def456", l.prompt)
	assert.Contains(t, c.added, ReviewStartedHeader)
}

func TestApplyTransitionReviewRetry(t *testing.T) {
	// SC-695: a stage-failed review ([human:review-failed], state failed) was a
	// dead end — the rework re-drop needs a DONE verification with a failing
	// verdict, and a failed review matches neither it nor any forward move. A
	// verification→verification drop on a failed card must relaunch the review
	// in place, re-bound to the handoff branch and commits.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
		cmt("[human:review-started]", time.Unix(2, 0)),
		cmt("[human:review-failed]\nbranch checkout failed", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardVerification})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-review HUM-9 --branch=feat/x --commits=abc123", l.prompt)
	assert.Equal(t, "board-SC-1-verification", l.name)
	// The prior stage (ready-for-review) finished decades before this retry — the
	// retry's cause (WaitCauseRetry) is over StageWaitThreshold, so it is
	// attributed with a [human:stage-wait] record ahead of the started marker
	// (SC-2462).
	require.Len(t, c.added, 2)
	assert.True(t, strings.HasPrefix(c.added[0], StageWaitHeader))
	assert.Equal(t, ReviewStartedHeader, c.added[1])
}

func TestApplyTransitionRunningReviewNotRelaunched(t *testing.T) {
	// Contract pin: review retry is for FAILED runs only — a running review hits
	// the idempotency guard and must not spawn a second agent (SC-695).
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-started]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardVerification})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplyTransitionIdempotentDuplicate(t *testing.T) {
	// An open started marker for the target stage makes the drop a no-op.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

// escalatedLoopThenRebuilt is the marker thread SC-1857 stranded: the PR
// review→fix loop escalated to a [human:options] block (which the escalation
// posts against the IMPLEMENTATION stage), the chosen rebuild ran, and its
// review passed. [human:pr-fix-started] is left as the newest done-stage marker
// forever — nothing in the loop ever closes the done stage.
func escalatedLoopThenRebuilt() []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:pr-review-started]\nnumber: 257\nbranch: autofix/x", time.Unix(1, 0)),
		cmt("[human:pr-fix-started]", time.Unix(2, 0)),
		cmt("[human:options]\nstage: implementation\ncontext: the fixer needs a decision\n1: Rebuild the branch\n2: Keep the branch and narrow the fix", time.Unix(3, 0)),
		cmt("[human:option-chosen] 1: Rebuild the branch", time.Unix(4, 0)),
		cmt("[human:implementation-started]", time.Unix(5, 0)),
		cmt("[human:ready-for-review]\nbranch: autofix/x\ncommits: abc1234", time.Unix(6, 0)),
		cmt("[human:review-started]", time.Unix(7, 0)),
		cmt("[human:review-complete]\nverdict: pass", time.Unix(8, 0)),
	}
}

func TestApplyTransitionDeployAfterEscalatedLoop(t *testing.T) {
	// SC-1857: the board derives this card to verification/done and offers the
	// Deploy drop, but the raw done stage still reads "running" off the stranded
	// [human:pr-fix-started]. Gating on the raw scan swallowed every drop with a
	// silent nil; gating on the derived card ships it.
	comments := escalatedLoopThenRebuilt()
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	require.Equal(t, BoardVerification, card.Stage, "board offers the Deploy drop")
	require.Equal(t, BoardDone, card.State)
	_, raw := latestStageState(comments, BoardDoneStage)
	require.Equal(t, BoardRunning, raw, "raw done stage disagrees — the trap")

	syncPRReview(t)
	c := &fakeCommenter{comments: comments}
	p := &fakeDeployer{res: PRResult{Number: 257, URL: "https://github.com/o/r/pull/257"}}
	deps := newDeps(c, &fakeLauncher{}, p)

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.NotEmpty(t, c.added, "the drop must do something visible, not return a silent nil")
}

func TestApplyTransitionRunningDeployStillIdempotent(t *testing.T) {
	// The other half of the contract: a genuinely in-flight loop — the same
	// thread WITHOUT the rebuild that moved past it — still derives to
	// done/running and must not launch a second deploy on a re-drop.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: autofix/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: pass", time.Unix(2, 0)),
		cmt("[human:pr-review-started]\nnumber: 257\nbranch: autofix/x", time.Unix(3, 0)),
		cmt("[human:pr-fix-started]", time.Unix(4, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{}
	deps := newDeps(c, l, p)

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Zero(t, p.call)
	assert.Empty(t, c.added)
}

// escalatedLoopAwaitingDecision is the SC-1857 thread at the moment the PR
// review→fix loop escalates to a [human:options] decision and BEFORE any human
// choice: the escalation posts its block against the implementation stage, and
// [human:pr-fix-started] is left as the newest done-stage marker. Nothing later
// exists yet to retire it.
func escalatedLoopAwaitingDecision() []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:pr-review-started]\nnumber: 257\nbranch: autofix/x", time.Unix(1, 0)),
		cmt("[human:pr-fix-started]", time.Unix(2, 0)),
		cmt("[human:options]\nstage: implementation\ncontext: the fixer needs a decision\n1: Rebuild the branch\n2: Keep the branch and narrow the fix", time.Unix(3, 0)),
	}
}

func TestDeriveBoardCardEscalatedLoopLeavesNoBlockingDoneMarker(t *testing.T) {
	// SC-1857 AC2: a loop escalated to options must leave no done-stage marker
	// that reads "running" and so blocks a later deploy. The stranded
	// [human:pr-fix-started] still scans raw as done/running, but the derived card
	// — the authority the board and the drop guard both read — pauses on the open
	// decision instead, exposing the options rather than a phantom deploy spinner.
	comments := escalatedLoopAwaitingDecision()

	_, raw := latestStageState(comments, BoardDoneStage)
	require.Equal(t, BoardRunning, raw, "the stranded loop marker still scans raw as running")

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardIdle, card.State, "the escalated loop pauses on its decision, it is not running")
	assert.False(t, isDuplicateDrop(BoardDoneStage, card), "no done-stage marker blocks a later deploy")
	assert.Empty(t, card.DeployPhase, "a paused decision is not a mid-flight deploy")
	require.NotEmpty(t, card.Options, "the decision surfaces on the card")
	assert.Equal(t, BoardImplementation, card.OptionsStage)
}

func TestApplyTransitionRefusesDropOnAwaitingDecisionCard(t *testing.T) {
	// SC-1857 AC3: dropping a card that is refusing the move must surface a reason,
	// never appear to do nothing. Before the fix this exact escalated thread derived
	// to done/running and the drop was swallowed by a silent duplicate-drop nil; now
	// the card is paused on its decision and the drop returns an actionable refusal.
	c := &fakeCommenter{comments: escalatedLoopAwaitingDecision()}
	l := &fakeLauncher{}
	p := &fakeDeployer{}
	deps := newDeps(c, l, p)

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.Error(t, err, "a refused drop must not return a silent nil")
	assert.Contains(t, err.Error(), "waiting on a decision", "the refusal names why, so the board can show it")
	assert.Zero(t, l.calls, "nothing is launched")
	assert.Zero(t, p.call, "nothing is deployed")
	assert.Empty(t, c.added, "a refused move posts no stage marker")
}

func TestApplyTransitionDoneNoBranch(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:review-complete]", time.Unix(1, 0))}}
	p := &fakeDeployer{}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Zero(t, p.call)
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], DeployFailedHeader)
}

// TestRunDoneStage_startsReviewLoop verifies the Done stage now fronts the deploy
// with the PR review→fix loop: it starts the review (opens a draft PR, launches
// the reviewer) rather than posting deploy-started and merging.
func TestRunDoneStage_startsReviewLoop(t *testing.T) {
	syncPRReview(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}}
	deps := newDeps(c, l, p)

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage}))

	assert.True(t, p.req.Draft, "the review loop opens the PR in draft state")
	assert.Equal(t, "board-SC-1-prreview", l.name, "the reviewer must be launched")
	assert.Zero(t, p.merged, "the loop must not merge before the review approves")
	for _, b := range c.added {
		assert.NotEqual(t, DeployStartedHeader, b, "the Done stage no longer posts deploy-started")
	}
}

func TestApplyTransitionDeploySuccess(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}, checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
	assert.Equal(t, "feat/x", p.req.Branch)
	assert.Equal(t, "My feature", p.req.Title)
	assert.Equal(t, 1, p.merged)
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Equal(t, "SC-1", closed)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/7")
}

// TestApplyTransitionDeployAlreadyMerged is the SC-911 regression: re-running
// Deploy on a card whose branch is already on main must be a clean no-op —
// never open a PR (which the forge rejects 422 "No commits between main and
// <branch>", redding a finished card), and instead end deployed/done and close
// the ticket. On the pre-fix deploy() (which calls PushAndCreatePR
// unconditionally) the branch reds; this test fails there.
func TestApplyTransitionDeployAlreadyMerged(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{alreadyMerged: true}
	deps := newDeps(c, &fakeLauncher{}, p)
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	// The already-merged short-circuit must skip the forge entirely.
	assert.Zero(t, p.call, "an already-merged branch must never open a PR")
	assert.Zero(t, p.merged, "an already-merged branch must never re-merge")
	assert.Empty(t, p.deleted)
	// The card ends deployed/done (green) and the ticket is closed.
	assert.Equal(t, "SC-1", closed)
	var deployed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployedHeader) {
			deployed = b
		}
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader),
			"an already-merged branch must never dead-end on deploy-failed: %q", b)
	}
	require.NotEmpty(t, deployed, "expected a deployed marker for the no-op")
	stage, state, ok := ClassifyMarker(deployed)
	require.True(t, ok, "the deployed marker must classify as a stage transition")
	assert.Equal(t, BoardDoneStage, stage)
	assert.Equal(t, BoardDone, state)
}

func TestApplyTransitionDeployCloseFails(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/11", Number: 11},
		checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	var closeCalls int
	deps.CloseTicket = func(pmKey string) error {
		closeCalls++
		return errors.New("tracker unavailable")
	}
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})

	// The deploy itself must succeed — the card never turns red.
	require.NoError(t, err)
	assert.Equal(t, 1, p.merged)
	// The work shipped: the deployed marker is still posted.
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/11")
	// The close was attempted, retried once, then surfaced.
	assert.Equal(t, 2, closeCalls)
	// The failure is surfaced on the ticket, flagged for manual close.
	var surfaced string
	for _, b := range c.added {
		if strings.HasPrefix(b, CloseFailedHeader) {
			surfaced = b
		}
	}
	require.NotEmpty(t, surfaced, "expected a close-failed marker on the ticket")
	assert.Contains(t, surfaced, "tracker unavailable")
	assert.Contains(t, surfaced, "SC-1")
	// The close-failed marker must NOT drive a stage/state transition (never reds).
	_, _, ok := ClassifyMarker(surfaced)
	assert.False(t, ok, "close-failed marker must not be a registered stage marker")
}

func TestCloseFailedHeaderUnregistered(t *testing.T) {
	_, _, ok := ClassifyMarker(CloseFailedHeader)
	assert.False(t, ok, "close-failed marker must never drive a stage/state transition")
}

func TestHandoffCheckUnreadableHeaderUnregistered(t *testing.T) {
	_, _, ok := ClassifyMarker(HandoffCheckUnreadableHeader)
	assert.False(t, ok, "handoff-check-unreadable marker must never drive a stage/state transition")
}

func TestApplyTransitionDeployWaitsForPendingChecks(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/8", Number: 8},
		checks: []forge.ChecksState{forge.ChecksPending, forge.ChecksPending, forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 3, p.checkCall)
	assert.Equal(t, 1, p.merged)
}

// TestApplyTransitionDeployCancelledCheckDoesNotFailGate is the daemon-layer
// companion to SC-2602: combineChecks now maps a cancelled (superseded) build
// to forge.ChecksPending instead of ChecksFailing, so from waitForChecks's
// perspective a cancelled build looks exactly like this Pending → Passing
// sequence. With a launcher wired (unlike TestApplyTransitionDeployChecksFail,
// which nils it out for the CLI-only path), this asserts the gate keeps
// polling and merges cleanly — no DeployFailedHeader, no fixer dispatch —
// rather than dead-ending on a build that was only called off, not failed.
func TestApplyTransitionDeployCancelledCheckDoesNotFailGate(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/12", Number: 12},
		checks: []forge.ChecksState{forge.ChecksPending, forge.ChecksPassing}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})

	require.NoError(t, err)
	assert.Equal(t, 1, p.merged)
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader),
			"a cancelled check-run must not be misread as a CI failure: %q", b)
	}
	assert.Zero(t, l.calls, "a superseded build must never dispatch the deploy-fixer")
}

func TestApplyTransitionDeployChecksFail(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/9", Number: 9}, checks: []forge.ChecksState{forge.ChecksFailing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	// Nil launcher = the CLI deploy path: a failing CI gate is terminal (AD4). The
	// launcher-wired dispatch path has its own test.
	deps.Launcher = nil
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err) // the transition itself succeeded; the failure is a marker
	assert.Zero(t, p.merged)
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Contains(t, failed, "CI checks failed")
	assert.Contains(t, failed, "pr: https://example/pr/9")
}

func TestApplyTransitionDeployMergeFails(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/10", Number: 10},
		checks: []forge.ChecksState{forge.ChecksPassing}, mergeErr: errors.New("merge conflict")}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Empty(t, p.deleted)
	var sawFailed bool
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) && strings.Contains(b, "merge conflict") {
			sawFailed = true
		}
	}
	assert.True(t, sawFailed)
}

// TestApplyTransitionDeployRebasesStaleBranch is the ticket-735 regression: a
// handoff branch that has fallen behind main must be made mergeable (rebased,
// re-pushed) by a freshness stage BEFORE the merge, instead of dead-ending on a
// terminal [human:deploy-failed]. mergeUntil models GitHub's 405 on the stale
// tip; the freshness stage clears it. On the pre-fix deploy() (no EnsureMergeable
// call) the merge stays conflicted and the card reds — this test fails there.
func TestApplyTransitionDeployRebasesStaleBranch(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/12", Number: 12},
		checks: []forge.ChecksState{forge.ChecksPassing}, mergeUntil: true}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	// The freshness stage ran once, before the merge.
	assert.Equal(t, 1, p.ensured, "EnsureMergeable must run exactly once before the merge")
	assert.Equal(t, 1, p.merged, "the branch must merge after being made mergeable")
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/12")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader),
			"a stale branch must be rebased and merged, never dead-end on deploy-failed: %q", b)
	}
}

// TestApplyTransitionDeployEnsureMergeableConflict covers a genuine end-state
// conflict: the mechanical rebase in EnsureMergeable fails AND the forge itself
// declines the merge (mergeable false). The deploy must NOT attempt the merge
// and must red the card with a mergeability reason (SC-804).
func TestApplyTransitionDeployEnsureMergeableConflict(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/13", Number: 13},
		checks:    []forge.ChecksState{forge.ChecksPassing},
		ensureErr: errors.New("rebase hit a conflict"), mergeable: false}
	deps := newDeps(c, &fakeLauncher{}, p)
	// Nil launcher = the CLI deploy path: no fixer to dispatch, so a conflict is
	// terminal (AD4). The launcher-wired dispatch path has its own test.
	deps.Launcher = nil
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Equal(t, 1, p.ensured)
	assert.Zero(t, p.merged, "a branch that could not be made mergeable must not be merged blind")
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	// The marker's first line is the card badge: it must tell the user the next
	// step, with the raw cause following in the detail block.
	headline := firstLine(marker.Prose(failed))
	assert.Contains(t, headline, "resolve the conflict on feat/x")
	assert.Contains(t, headline, "re-run Deploy")
	assert.Contains(t, failed, "rebase hit a conflict")
}

// TestApplyTransitionDeployRebaseConflictForgeMergeableFallback is the SC-804
// regression: the mechanical rebase in EnsureMergeable conflicts on an
// intermediate commit the forge's end-state three-way merge never sees, yet the
// forge reports the PR mergeable and CI is green on the (rebase-aborted,
// unchanged) tip. The deploy must fall back to the forge verdict and proceed to
// the real merge instead of redding the card. On the pre-fix deploy() (which
// reds on any EnsureMergeable error) the card reds and no merge happens — this
// test fails there.
func TestApplyTransitionDeployRebaseConflictForgeMergeableFallback(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/14", Number: 14},
		checks:    []forge.ChecksState{forge.ChecksPassing},
		ensureErr: errors.New("rebasing branch onto base"), mergeable: true}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.ensured, "the freshness stage must still run once")
	assert.Equal(t, 1, p.merged, "a forge-mergeable, green-CI PR must merge despite the rebase conflict")
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/14")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader),
			"a forge-mergeable PR must merge, never dead-end on deploy-failed: %q", b)
	}
}

// TestIsDeployRetry pins the retry predicate: a failed done stage re-dropped on
// Deploy is a rebase-and-redeploy, not a dead end.
func TestIsDeployRetry(t *testing.T) {
	assert.True(t, isDeployRetry(BoardDoneStage, BoardCard{Stage: BoardDoneStage, State: BoardFailed}))
	assert.False(t, isDeployRetry(BoardDoneStage, BoardCard{Stage: BoardDoneStage, State: BoardRunning}))
	assert.False(t, isDeployRetry(BoardDoneStage, BoardCard{Stage: BoardVerification, State: BoardFailed}))
	assert.False(t, isDeployRetry(BoardVerification, BoardCard{Stage: BoardDoneStage, State: BoardFailed}))
}

// An outage card is relaunched in place exactly like a failed one, so the
// reconcile backoff can re-drive it (SC-2307). Every relaunchable stage's
// predicate must accept BoardOutage alongside BoardFailed.
func TestRetryPredicates_AcceptOutage(t *testing.T) {
	assert.True(t, isBuildRetry(BoardImplementation, BoardCard{Stage: BoardImplementation, State: BoardOutage}))
	assert.True(t, isReviewRetry(BoardVerification, BoardCard{Stage: BoardVerification, State: BoardOutage}))
	assert.True(t, isDeployRetry(BoardDoneStage, BoardCard{Stage: BoardDoneStage, State: BoardOutage}))
	assert.True(t, isPlanningRetry(BoardPlanning, BoardCard{Stage: BoardPlanning, State: BoardOutage}))

	// isReworkTransition is the backward rework move keyed on BoardDone — an
	// outage must NOT trigger it (SC-2307 D4).
	assert.False(t, isReworkTransition(BoardImplementation, BoardCard{Stage: BoardVerification, State: BoardOutage}))
}

// outageHeaderFor mirrors failedHeaderFor: one header per relaunchable stage,
// empty for a stage with no relaunch path.
func TestOutageHeaderFor(t *testing.T) {
	assert.Equal(t, PlanningOutageHeader, outageHeaderFor(BoardPlanning))
	assert.Equal(t, ImplementationOutageHeader, outageHeaderFor(BoardImplementation))
	assert.Equal(t, ReviewOutageHeader, outageHeaderFor(BoardVerification))
	assert.Equal(t, DeployOutageHeader, outageHeaderFor(BoardDoneStage))
	assert.Empty(t, outageHeaderFor(BoardBacklog))
}

// TestApplyTransitionDeployRetryRebasesAndRedeploys drives the whole retry path:
// a card sitting on a failed deploy, re-dropped on Deploy, must re-run the
// deploy pipeline (rebase + merge) rather than being rejected by the
// forward-only rule.
func TestApplyTransitionDeployRetryRebasesAndRedeploys(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
		cmt(DeployFailedHeader+"\nPull Request has merge conflicts", time.Unix(3, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/14", Number: 14},
		checks: []forge.ChecksState{forge.ChecksPassing}, mergeUntil: true}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.ensured)
	assert.Equal(t, 1, p.merged)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/14")
}

func TestApplyTransitionDonePushFails(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{prErr: errors.New("push rejected")}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err) // async pipeline: the push failure lands as a marker
	var sawFailed bool
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) && strings.Contains(b, "push rejected") {
			sawFailed = true
		}
	}
	assert.True(t, sawFailed)
}

func TestStartAgentStageLaunchFails(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: errors.New("docker down")}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.Error(t, err)
	// started marker posted, then failed marker posted on launch error.
	require.Len(t, c.added, 2)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
	assert.Contains(t, c.added[1], PlanningFailedHeader)
}

func TestStartAgentStageAlreadyRunningIsNoOp(t *testing.T) {
	// A retry that races the daemon's agent cleanup hits the manager's
	// single-flight guard, which refuses the second launch with
	// ErrAgentAlreadyRunning. That benign refusal must leave the card running:
	// no [human:*-failed] marker, nil return (SC-1419).
	c := &fakeCommenter{}
	l := &fakeLauncher{err: ErrAgentAlreadyRunning}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.NoError(t, err)
	// exactly the started marker, and no failed marker.
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
	for _, body := range c.added {
		assert.NotContains(t, body, PlanningFailedHeader)
	}
}

func TestDispatchDeployFixerAlreadyRunningIsNoOp(t *testing.T) {
	// The deploy gate racing its own repair hits the single-flight guard, which
	// refuses the second launch with ErrAgentAlreadyRunning. That benign refusal
	// must leave the card spinning on the deploy-fix-started marker: no
	// [human:deploy-failed] marker, nil return (SC-2603, mirrors SC-1419).
	c := &fakeCommenter{}
	l := &fakeLauncher{err: ErrAgentAlreadyRunning}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.dispatchDeployFixer(context.Background(), "SC-1",
		PRResult{URL: "https://example/pr/7", Number: 7}, "feat/x", "CI failed")
	require.NoError(t, err)
	require.Equal(t, 1, l.calls, "the launch must have been attempted")
	// exactly the running deploy-fix-started marker, and no deploy-failed marker.
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], DeployFixStartedHeader))
	for _, body := range c.added {
		assert.NotContains(t, body, DeployFailedHeader)
	}
}

func TestLaunchPRLoopAgentAlreadyRunningIsNoOp(t *testing.T) {
	// The PR review→fix loop racing its own agent hits the same single-flight
	// guard. The benign refusal must leave the loop's card running: no
	// [human:pr-review-failed] marker, nil return (SC-2603).
	c := &fakeCommenter{}
	l := &fakeLauncher{err: ErrAgentAlreadyRunning}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.launchPRLoopAgent(context.Background(), "SC-1", prReviewAgentStage, "/human-pr-review SC-1")
	require.NoError(t, err)
	require.Equal(t, 1, l.calls, "the launch must have been attempted")
	assert.Empty(t, c.added, "a benign single-flight refusal posts no marker")
	for _, body := range c.added {
		assert.NotContains(t, body, PRReviewFailedHeader)
	}
}

func TestAgentNameRoundTrip(t *testing.T) {
	name := agentNameFor("SC-105", BoardImplementation)
	assert.Equal(t, "board-SC-105-implementation", name)
	pm, stage, ok := parseAgentName(name)
	require.True(t, ok)
	assert.Equal(t, "SC-105", pm)
	assert.Equal(t, BoardImplementation, stage)
}

func TestParseAgentNameRejectsMalformed(t *testing.T) {
	cases := []string{
		"agent-1",       // wrong prefix
		"board-",        // no key/stage
		"board-onlykey", // no trailing stage segment
		"board--done",   // empty key segment
	}
	for _, name := range cases {
		_, _, ok := parseAgentName(name)
		assert.False(t, ok, "name %q should not parse", name)
	}
}

// listErrCommenter fails ListComments to exercise ApplyTransition's load-error path.
type listErrCommenter struct{ *fakeCommenter }

func (listErrCommenter) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, errors.New("tracker unreachable")
}

func TestApplyTransitionListCommentsError(t *testing.T) {
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, &fakeDeployer{})
	deps.Commenter = listErrCommenter{&fakeCommenter{}}
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.Error(t, err)
}

func TestStartAgentStageStartedMarkerError(t *testing.T) {
	// AddComment failing on the started marker aborts before launch.
	c := &fakeCommenter{addErr: errors.New("comment api down")}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestFailedHeaderFor(t *testing.T) {
	assert.Equal(t, PlanningFailedHeader, failedHeaderFor(BoardPlanning))
	assert.Equal(t, ImplementationFailedHeader, failedHeaderFor(BoardImplementation))
	assert.Equal(t, ReviewFailedHeader, failedHeaderFor(BoardVerification))
	assert.Equal(t, DeployFailedHeader, failedHeaderFor(BoardDoneStage))
	assert.Equal(t, "", failedHeaderFor(BoardBacklog))
}

func TestApplyTransitionImplementationWithoutEngineeringKey(t *testing.T) {
	// Single-tracker topology: no engineering: line anywhere — the plan is a
	// [human:plan] comment, so the executor is dispatched on the PM key.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan]\nthe plan", time.Unix(1, 0)),
		cmt("[human:plan-ready]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-execute SC-1")
	assert.Contains(t, l.prompt, "BOARD CONTEXT", "headless dispatch must carry the no-push, no-questions rules")
}

func TestApplyTransitionVerificationWithoutEngineeringKey(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})
	require.NoError(t, err)
	// SC-695: single-tracker topology dispatches on the PM key and still carries
	// the handoff branch binding.
	assert.Equal(t, "/human-review SC-1 --branch=feat/x", l.prompt)
}

func TestDoneBodySingleRef(t *testing.T) {
	// Regression: without an engineering ticket the PR body carries only the
	// PM line — no empty "Engineering ticket:" placeholder.
	body := doneBody("SC-1", BoardCard{Branch: "feat/x"})
	assert.Contains(t, body, "PM ticket: SC-1")
	assert.NotContains(t, body, "Engineering ticket:")
}

func TestApplyTransitionReworkAfterFailedVerdict(t *testing.T) {
	// The one sanctioned backward move: a build whose review failed may be
	// rebuilt, dispatched with a pointer at the review findings.
	c := &fakeCommenter{comments: []tracker.Comment{
		// The plan the original build carried out — its presence is what lets the
		// rework rebuild pass the implementation launch's plan gate (SC-2596).
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(2, 0)),
		cmt("[human:review-complete]\nverdict: fail\n\nmissing error handling", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-execute SC-1")
	assert.Contains(t, l.prompt, "review found problems")
	assert.Contains(t, c.added, ImplementationStartedHeader)
}

func TestApplyTransitionReworkAllowedWhenNoBranchRecorded(t *testing.T) {
	// Regression (SC-297): a passed review whose run never recorded a branch
	// has nothing to ship — the only repair is a rebuild, so the backward move
	// onto the build stage must be allowed exactly like a failed verdict.
	c := &fakeCommenter{comments: []tracker.Comment{
		// Plan evidence from the original build, so the rebuild clears the plan gate
		// (SC-2596); the rework itself is triggered by the branch-less passed review.
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: pass", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-execute SC-1")
}

func TestApplyTransitionReworkRejectedWithoutFailedVerdict(t *testing.T) {
	// Backward to implementation stays forbidden when the review passed.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: pass", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestReviewFailedDerivesToVerificationFailed(t *testing.T) {
	// A [human:review-failed] marker (the honest channel for "could not obtain
	// the code") reds the verification stage WITHOUT recording a verdict — so
	// the rework path, which keys on a failed verdict on a DONE card, never
	// fires against phantom findings (ticket 653).
	comments := []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-failed]\nhandoff branch feat/x not found — no code was reviewed", time.Unix(2, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardVerification, card.Stage)
	assert.Equal(t, BoardFailed, card.State)
	assert.Empty(t, card.Verdict, "review-failed is a stage failure, not a review verdict")
	assert.False(t, isReworkTransition(BoardImplementation, card),
		"a review-failed card must not qualify for the rework-to-implementation path")
}

func TestApplyTransitionReviewFailedDoesNotDispatchFixer(t *testing.T) {
	// Dropping a review-failed card toward Implementation must not launch a
	// fixer against findings that do not exist: the honest failure is retryable
	// in place (re-run the review), not a rework trigger (ticket 653).
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-failed]\nhandoff branch feat/x not found — no code was reviewed", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls, "no fixer may be dispatched for an unreviewable stage failure")
	assert.NotContains(t, c.added, ImplementationStartedHeader)
}

func TestApplyTransitionDeployBlockedByFailedVerdict(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: fail", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Zero(t, p.call)
}

func TestApplyTransitionDeployBlockedByIncompleteVerdict(t *testing.T) {
	// Regression (SC-2848): a review recording an unmet acceptance criterion as
	// the "incomplete" verdict must NOT reach Ready-to-Deploy — partial delivery
	// cannot merge. Mirrors the failing-verdict block.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: incomplete", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Zero(t, p.call)
}

func TestApplyTransitionDeployAllowedWithPassWithNotes(t *testing.T) {
	syncPRReview(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: pass with notes", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/11", Number: 11}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	// pass-with-notes is not a failing verdict: the deploy is not blocked, so the
	// review loop is entered — a draft PR opens and the reviewer is launched.
	assert.Equal(t, 1, p.call)
	assert.True(t, p.req.Draft)
	assert.Equal(t, "board-SC-1-prreview", l.name)
	var sawReviewStarted bool
	for _, b := range c.added {
		if strings.HasPrefix(b, PRReviewStartedHeader) {
			sawReviewStarted = true
		}
	}
	assert.True(t, sawReviewStarted, "expected a pr-review-started marker")
}

func TestVerdictFailed(t *testing.T) {
	assert.True(t, VerdictFailed("fail"))
	assert.True(t, VerdictFailed("  FAILED — see findings"))
	// "incomplete" — built correctly, but not everything the ticket asked for —
	// blocks the merge exactly like a fail so partial delivery cannot ride
	// "pass with notes" through to deploy (SC-2848).
	assert.True(t, VerdictFailed("incomplete"))
	assert.True(t, VerdictFailed("  Incomplete — criterion 5 (per-ticket view) unmet"))
	assert.False(t, VerdictFailed("pass"))
	assert.False(t, VerdictFailed("pass with notes"))
	// Absence of a verdict is not failure — pre-verdict threads keep flowing.
	assert.False(t, VerdictFailed(""))
}

func TestApplyTransitionIdeasGuard(t *testing.T) {
	// Ideas leave their column via ideation's label swap, never via a board
	// transition — both directions are rejected before any comment fetch.
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardIdeas, To: BoardBacklog})
	require.Error(t, err)
	err = deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardIdeas})
	require.Error(t, err)
}

func TestApplyFixLaunchesAutofix(t *testing.T) {
	// A backlog bug goes straight to the fix: no planning gate, the autofix
	// pipeline triages and plans itself.
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9", PMTitle: "Crash on save"})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-autofix SC-9 --board", l.prompt)
	// The implementation-stage agent name keeps the failure watcher and the
	// build→review chain working on bug fixes unchanged.
	assert.Equal(t, "board-SC-9-implementation", l.name)
	// The started marker, then the durable pipeline-identity marker a recovery
	// relaunch reads to restart the FIX pipeline (SC-2989).
	require.Len(t, c.added, 2)
	assert.Equal(t, ImplementationStartedHeader, c.added[0])
	assert.Equal(t, PipelineStartedHeader+"\nkind: fix", c.added[1])
}

func TestApplyFixLaunchPromptCarriesBoardMarker(t *testing.T) {
	// Regression (SC-252): a board-launched autofix must never push or open a
	// PR from its credential-less container — the daemon's Deploy stage owns
	// push+PR+merge. Board context must be a MECHANICAL signal the skill and
	// fixer branch on, injected into the launch prompt, not left to the agent
	// noticing HUMAN_AGENT_NAME. Assert the launch prompt carries the explicit
	// --board marker so the skill stops at the handoff and the fixer leaves the
	// branch local.
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9", PMTitle: "Crash on save"})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "--board",
		"board-launched autofix prompt must carry an explicit board marker so push/PR are skipped")
}

func TestApplyFixIdempotentWhileRunning(t *testing.T) {
	// A re-drop while the fix agent still runs must not launch a second one.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9"})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplyFixIdempotentWhileReviewRunning(t *testing.T) {
	// The fix chains into its review; a drop during that review is a no-op too.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: autofix/sc-9", time.Unix(1, 0)),
		cmt("[human:review-started]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9"})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
}

func TestApplyFixIdempotentWithStaleDeployFailure(t *testing.T) {
	// Regression (SC-230): a stale [human:deploy-failed] (older) pins
	// DeriveBoardCard to the done stage's failed state, masking a live
	// [human:implementation-started] (newer). The duplicate-launch guard must
	// scan the implementation stage itself, so a Retry click while the fix
	// agent runs is a no-op: zero launches, zero marker spam.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:deploy-failed]\nno forge configured", time.Unix(1, 0)),
		cmt("[human:implementation-started]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9"})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplyFixRelaunchAfterFailedReview(t *testing.T) {
	// A bug pinned by a failing review verdict may be re-dropped onto Fix.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: autofix/sc-9", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: fail", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9"})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-autofix SC-9 --board", l.prompt)
}

func TestApplyFixLaunchFailurePostsFailedMarker(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: errors.New("no docker")}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-9"})
	require.Error(t, err)
	require.Len(t, c.added, 2)
	assert.Equal(t, ImplementationStartedHeader, c.added[0])
	assert.True(t, strings.HasPrefix(c.added[1], ImplementationFailedHeader))
}

func TestApplySecurityFixLaunchesSecurityFix(t *testing.T) {
	// A security ticket goes straight to the fix: no planning gate, the
	// security-fix pipeline triages and plans itself. It launches under the
	// implementation-stage agent name so the failure watcher and the
	// build→review chain apply to a security fix unchanged — exactly like a bug.
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplySecurityFix(context.Background(), SecurityFixRequest{PMKey: "SC-9", PMTitle: "Auth token leaks"})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-security-fix SC-9 --board", l.prompt)
	assert.Equal(t, "board-SC-9-implementation", l.name)
	// The started marker, then the durable pipeline-identity marker (kind: security)
	// a recovery relaunch reads to restart the security-fix pipeline (SC-2989).
	require.Len(t, c.added, 2)
	assert.Equal(t, ImplementationStartedHeader, c.added[0])
	assert.Equal(t, PipelineStartedHeader+"\nkind: security", c.added[1])
}

func TestApplySecurityFixCarriesBoardMarker(t *testing.T) {
	// Same credential-less-container constraint as a board autofix (SC-252): the
	// launch prompt must carry the explicit --board marker so the skill stops at
	// the review handoff and never pushes from the container.
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplySecurityFix(context.Background(), SecurityFixRequest{PMKey: "SC-9", PMTitle: "Auth token leaks"})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "--board")
}

func TestApplySecurityFixIdempotentWhileRunning(t *testing.T) {
	// A re-drop while the fix agent still runs must not launch a second one.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplySecurityFix(context.Background(), SecurityFixRequest{PMKey: "SC-9"})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplySecurityFixIdempotentWhileReviewRunning(t *testing.T) {
	// The fix chains into its review; a drop during that review is a no-op too.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: autofix/sc-9", time.Unix(1, 0)),
		cmt("[human:review-started]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplySecurityFix(context.Background(), SecurityFixRequest{PMKey: "SC-9"})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
}

// gateProbeDeployer reports when a pipeline enters the forge and holds it
// there until released, so a test can observe whether a second pipeline gets
// in while the first is still deploying.
type gateProbeDeployer struct {
	started chan string
	release chan struct{}
}

func (f *gateProbeDeployer) PushAndCreatePR(_ context.Context, req PRRequest) (PRResult, error) {
	f.started <- req.Branch
	<-f.release
	return PRResult{Number: 1, URL: "pr"}, nil
}

func (f *gateProbeDeployer) PullRequestChecks(_ context.Context, _ string, _ int) (forge.ChecksState, error) {
	return forge.ChecksPassing, nil
}

func (f *gateProbeDeployer) ReadPullRequest(_ context.Context, _ string, _ int) (*forge.PullRequestState, error) {
	return nil, nil
}

func (f *gateProbeDeployer) EnsureMergeable(_ context.Context, _ PRRequest) (bool, error) {
	return false, nil
}

func (f *gateProbeDeployer) PullRequestMergeable(_ context.Context, _ string, _ int) (bool, error) {
	return true, nil
}

func (f *gateProbeDeployer) MergePullRequest(_ context.Context, _ string, _ int) error { return nil }

func (f *gateProbeDeployer) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }

func (f *gateProbeDeployer) BranchMerged(_ context.Context, _, _ string) bool { return false }

func (f *gateProbeDeployer) MarkReadyForReview(_ context.Context, _ string, _ int) error { return nil }

func (f *gateProbeDeployer) PublishResolvedBranch(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func TestDeploysQueueOneAtATime(t *testing.T) {
	// Regression (SC-296): the Deploy button ships every ready fix at once.
	// Concurrent pipelines race the mainline — the first merge moves the base
	// branch and the forge rejects the rest ("base branch was modified") — so
	// pipelines must queue: the second may not enter the forge while the first
	// is still deploying.
	f := &gateProbeDeployer{started: make(chan string, 2), release: make(chan struct{})}
	deps := BoardTransitionDeps{Commenter: &fakeCommenter{}, Deployer: f, WorkspaceDir: "/ws", ConfigDir: "/ws"}

	var done sync.WaitGroup
	done.Add(2)
	for _, branch := range []string{"autofix/one", "autofix/two"} {
		go func(b string) {
			defer done.Done()
			deps.deploy(context.Background(), BoardTransitionRequest{PMKey: "SC-9"}, BoardCard{Branch: b})
		}(branch)
	}

	first := <-f.started
	select {
	case second := <-f.started:
		t.Fatalf("deploy of %s entered the forge while %s was still deploying", second, first)
	case <-time.After(100 * time.Millisecond):
	}

	close(f.release)
	assert.NotEqual(t, first, <-f.started, "the queued deploy must run after the first lands")
	done.Wait()
}

// TestApplyTransitionDeployWaitsOutMergeabilityRecompute covers the race that
// redded ticket 910's card: after the freshness rebase re-pushes the branch,
// the forge recomputes mergeability asynchronously and the merge endpoint 405s
// until it settles. A deploy that rebased must poll the verdict and merge only
// once it turns true — never fail on the transient window.
func TestApplyTransitionDeployWaitsOutMergeabilityRecompute(t *testing.T) {
	syncDeploy(t)
	origInterval, origTimeout := mergeablePollInterval, mergeablePollTimeout
	mergeablePollInterval, mergeablePollTimeout = time.Millisecond, time.Second
	t.Cleanup(func() { mergeablePollInterval, mergeablePollTimeout = origInterval, origTimeout })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/14", Number: 14},
		checks:  []forge.ChecksState{forge.ChecksPassing},
		rebased: true, mergeableAfter: 3}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.merged, "merge must proceed once the recompute settles")
	assert.GreaterOrEqual(t, p.mergeableCalls, 3, "the verdict must be polled through the recompute window")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "transient recompute must not red the card: %s", b)
	}
}

// TestApplyTransitionDeployRecomputeStaysUnmergeable: when the verdict never
// turns true, the failure marker must lead with an actionable headline.
func TestApplyTransitionDeployRecomputeStaysUnmergeable(t *testing.T) {
	syncDeploy(t)
	origInterval, origTimeout := mergeablePollInterval, mergeablePollTimeout
	mergeablePollInterval, mergeablePollTimeout = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { mergeablePollInterval, mergeablePollTimeout = origInterval, origTimeout })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/15", Number: 15},
		checks:  []forge.ChecksState{forge.ChecksPassing},
		rebased: true, mergeable: false}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Zero(t, p.merged)
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline := firstLine(marker.Prose(failed))
	assert.Contains(t, headline, "open the PR to see why")
	assert.Contains(t, headline, "re-run Deploy")
}

// TestApplyTransitionDeployReGatesCIAfterRebase is the SC-1184 regression: the
// freshness rebase force-pushes a new head, re-triggering CI. On the new head
// GitHub reports mergeable_state unstable and 405s the merge while those fresh
// checks are still in_progress. The deploy must re-gate CI on the rebased head
// (waitForChecks) before attempting the merge — not merge on the stale green.
// The pre-fix rebased block polls only mergeability, never re-runs the CI gate,
// so it merges into the fresh-CI window, draws the 405, and reds the card: this
// test fails there.
func TestApplyTransitionDeployReGatesCIAfterRebase(t *testing.T) {
	syncDeploy(t)
	origInterval, origTimeout := mergeablePollInterval, mergeablePollTimeout
	mergeablePollInterval, mergeablePollTimeout = time.Millisecond, time.Second
	t.Cleanup(func() { mergeablePollInterval, mergeablePollTimeout = origInterval, origTimeout })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	// Pre-rebase CI is green, then the rebase re-pushes a new head whose fresh CI
	// is in_progress (pending) before it settles green. The forge 405s the merge
	// until that fresh CI is re-gated.
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/17", Number: 17},
		checks: []forge.ChecksState{
			forge.ChecksPassing,
			forge.ChecksPending, forge.ChecksPending, forge.ChecksPassing,
		},
		rebased: true, mergeable: true, mergeBlockedUntilRegate: true}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	// The fresh CI on the rebased head must be re-gated: more than the single
	// pre-rebase poll, and settled on Passing at least twice.
	assert.GreaterOrEqual(t, p.checkCall, 4, "CI must be re-gated on the rebased head, not merged on stale green")
	assert.GreaterOrEqual(t, p.checksPassed, 2, "the fresh CI on the rebased head must reconclude green before the merge")
	assert.Equal(t, 1, p.merged, "the merge fires once, after the fresh CI re-gate")
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/17")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader),
			"a rebased head must re-gate CI and merge, never dead-end on the fresh-CI 405: %q", b)
	}
}

// TestApplyTransitionDeployRetriesTransientMergeRefusal covers the second half
// of the SC-1184 fix: a transient 405 "not mergeable" (the forge reporting the
// head unstable/behind for a beat) must be ridden out with bounded backoff, not
// treated as terminal. Here CI is green and the branch is current, yet the first
// two merge attempts 405 before the forge accepts the merge.
func TestApplyTransitionDeployRetriesTransientMergeRefusal(t *testing.T) {
	syncDeploy(t)
	origInterval, origTimeout := mergeRetryInterval, mergeRetryTimeout
	mergeRetryInterval, mergeRetryTimeout = time.Millisecond, time.Second
	t.Cleanup(func() { mergeRetryInterval, mergeRetryTimeout = origInterval, origTimeout })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/18", Number: 18},
		checks: []forge.ChecksState{forge.ChecksPassing}, mergeTransientUntil: 2}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 3, p.merged, "the merge must retry through the transient 405 until it lands")
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/18")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader),
			"a transient merge refusal must be retried, never dead-end the card: %q", b)
	}
}

// TestApplyTransitionDeployTransientMergeRefusalTimesOut pins the bound: a 405
// that never clears is not retried forever — once the retry window elapses the
// card reds with the merge-refused headline.
func TestApplyTransitionDeployTransientMergeRefusalTimesOut(t *testing.T) {
	syncDeploy(t)
	origInterval, origTimeout := mergeRetryInterval, mergeRetryTimeout
	mergeRetryInterval, mergeRetryTimeout = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { mergeRetryInterval, mergeRetryTimeout = origInterval, origTimeout })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/19", Number: 19},
		checks: []forge.ChecksState{forge.ChecksPassing}, mergeErr: errors.New("405 Pull Request is not mergeable")}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Empty(t, p.deleted, "an unmerged branch must not be deleted")
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline := firstLine(marker.Prose(failed))
	assert.Contains(t, headline, "the forge refused the merge")
	assert.Contains(t, headline, "re-run Deploy")
}

// TestApplyTransitionDeployCIFailureHeadline: a failing CI gate must red the
// card with a fix-the-checks instruction, not a raw error chain.
func TestApplyTransitionDeployCIFailureHeadline(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/16", Number: 16},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	// Nil launcher = the CLI deploy path: a failing CI gate is terminal (AD4). The
	// launcher-wired dispatch path has its own test.
	deps.Launcher = nil
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline := firstLine(marker.Prose(failed))
	assert.Contains(t, headline, "fix the failing checks")
	assert.Contains(t, headline, "re-run Deploy")
}

func TestApplyTransitionReplansDonePlanning(t *testing.T) {
	// A finished plan sitting in the Engineering backlog can rot while the
	// codebase moves on. The Replan gesture relaunches planning in place; the
	// fresh plan supersedes the old one by the plan layer's latest-wins rule.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:planning-started]", time.Unix(1, 0)),
		cmt("[human:plan-ready]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardPlanning})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-ticket-review SC-1")
	assert.Contains(t, l.prompt, "/human-plan SC-1")
	// The prior stage (plan-ready) finished decades before this replan — the
	// retry's cause (WaitCauseRetry) is over StageWaitThreshold, so it is
	// attributed with a [human:stage-wait] record ahead of the started marker
	// (SC-2462).
	require.Len(t, c.added, 2)
	assert.True(t, strings.HasPrefix(c.added[0], StageWaitHeader))
	assert.Equal(t, PlanningStartedHeader, c.added[1])
}

func TestApplyTransitionReplanRejectedBeyondPlanning(t *testing.T) {
	// Replan is scoped to the Engineering backlog: a card already in
	// implementation keeps the forward-only rule for To=planning.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:implementation-started]", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardPlanning})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

// deployFixReadyComments is the standard review-complete thread a deploy runs
// against: a branch binding plus the review-complete gate that lets the deploy
// pipeline proceed to its CI/merge steps.
func deployFixReadyComments() []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}
}

// A code-fixable CI failure at the deploy gate, with a launcher wired (the board
// path), dispatches the deploy-fixer and keeps the card spinning instead of
// redding — the whole point of SC-1557.
func TestDeployBranch_CIFailure_DispatchesFixer(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err, "a dispatched fixer releases the deploy gate rather than erroring")

	var started string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "a dispatched fixer must not red the card: %q", b)
	}
	require.NotEmpty(t, started, "the running deploy-fix-started marker must be posted")
	assert.Contains(t, started, "number: 7")
	assert.Contains(t, started, "branch: feat/x")
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "board-SC-1-deployfix", l.name)
	assert.Equal(t, "/human-deploy-fix SC-1 --pr=7 --branch=feat/x", l.prompt)
	assert.Zero(t, p.merged, "the deploy must not merge a CI-failed head")
}

// SC-2042: a secret-store auth failure surfaced at the CI gate must be reported
// as an authentication problem and must NOT be handed to the code fixer — the
// suite is green; there is nothing to fix. Before the fix, ciFailureFixable
// returns true for this error and a fixer is dispatched (this test fails RED).
func TestDeployBranch_SecretStoreAuthFailure_NotFixable(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	authErr := humanerrors.WrapWithDetails(vault.ErrNotAuthenticated,
		"1Password CLI op could not read 1pw://Private/Shortcut Token/notesPlain: not signed in")
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}, checksErr: authErr}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	assert.Zero(t, l.calls, "an auth failure is not code-fixable — no fixer may be dispatched")
	assert.Zero(t, p.merged, "a failed secret read must not merge")

	var failed, started string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
	}
	require.NotEmpty(t, failed, "the failure must red the card with a deploy-failed marker")
	assert.Empty(t, started, "no deploy-fix must be started")
	headline := firstLine(marker.Prose(failed))
	assert.Contains(t, strings.ToLower(headline), "authenticat",
		"the headline must state authentication is the problem")
	assert.NotContains(t, headline, "CI checks failed",
		"a failed secret read must not be reported as a CI failure")
}

// SC-2042: the same error on the push/PR path must be reported as a secret-store
// problem, not as "check the branch and forge access".
func TestDeployBranch_SecretStoreAuthFailure_PushPath(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	authErr := humanerrors.WrapWithDetails(vault.ErrNotAuthenticated,
		"GitHub CLI gh is not logged in")
	p := &fakeDeployer{prErr: authErr}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline := firstLine(marker.Prose(failed))
	assert.Contains(t, strings.ToLower(headline), "authenticat")
	assert.NotContains(t, headline, "check the branch and forge access")
}

// The CLI deploy path wires no launcher: a failing CI gate stays terminal (AD4),
// exactly as before SC-1557 — a human is at the CLI and fixes it directly.
func TestDeployBranch_CIFailure_NilLauncher_Reds(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	deps.Launcher = nil
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed, started string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Empty(t, started, "no fixer is dispatched on the CLI path")
}

// A CI *timeout* is an infra/slowness signal, not a code defect: even with a
// launcher wired the card reds and no fixer is dispatched (AD3, ciFailureFixable).
func TestDeployBranch_CITimeout_Reds(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/8", Number: 8},
		checksErr: errors.New("timed out waiting for CI checks")}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Zero(t, l.calls, "a CI timeout must not dispatch a code fixer")
}

// A genuine end-state conflict (mechanical rebase fails AND the forge declines
// the merge) dispatches the fixer to rebase and resolve it, rather than redding.
func TestDeployBranch_EnsureMergeableConflict_DispatchesFixer(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/13", Number: 13},
		checks:    []forge.ChecksState{forge.ChecksPassing},
		ensureErr: errors.New("rebase hit a conflict"), mergeable: false}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)

	var started, failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, started, "the conflict must dispatch the fixer")
	assert.Empty(t, failed)
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-deploy-fix SC-1 --pr=13 --branch=feat/x", l.prompt)
	assert.Zero(t, p.merged, "a branch that could not be made mergeable must not be merged blind")
}

// Once the per-ticket deploy-fix budget is spent (two prior rounds), a further
// failure reds instead of dispatching a third fixer — the loop is bounded (AD2).
func TestDeployBranch_BudgetExhausted_Reds(t *testing.T) {
	syncDeploy(t)
	comments := deployFixReadyComments()
	comments = append(comments,
		cmt(DeployFixStartedHeader+"\nround 1", time.Unix(3, 0)),
		cmt(DeployFixStartedHeader+"\nround 2", time.Unix(4, 0)))
	c := &fakeCommenter{comments: comments}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/9", Number: 9},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed, "a spent budget reds the card")
	assert.Zero(t, l.calls, "no third fixer is dispatched once the budget is spent")
}

// A launcher that fails to start the fixer reds the card (a spinning marker with
// no container behind it would strand the deploy).
func TestDeployBranch_FixerLaunchError_Reds(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/9", Number: 9},
		checks: []forge.ChecksState{forge.ChecksFailing}}
	l := &fakeLauncher{err: errors.New("boom")}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var startedIdx, failedIdx = -1, -1
	for i, b := range c.added {
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			startedIdx = i
		}
		if strings.HasPrefix(b, DeployFailedHeader) {
			failedIdx = i
		}
	}
	require.GreaterOrEqual(t, startedIdx, 0, "the fixer dispatch is attempted before the launch fails")
	require.GreaterOrEqual(t, failedIdx, 0, "the launch failure reds the card")
	assert.Greater(t, failedIdx, startedIdx, "the deploy-failed marker follows the deploy-fix-started marker")
}

// On the fixer's `done` exit the deploy re-runs end to end — the fixer rebased
// and fixed on the local branch, the daemon publishes it, and it is ready for a
// fresh CI gate + merge.
func TestAdvanceDeployFix_Done_RerunsDeploy(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/13", Number: 13},
		checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", ExitDone)
	require.NoError(t, err)
	assert.Equal(t, 1, p.merged, "a done fixer re-runs the deploy through to the merge")

	var deployed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployedHeader) {
			deployed = b
		}
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "a done exit must not red the card: %q", b)
	}
	require.NotEmpty(t, deployed, "the re-run deploy posts the deployed marker")
}

// The fixer resolves the conflict in a container that holds no push credentials,
// so its deliverable is the LOCAL branch: the daemon must carry it to origin
// before re-running the deploy. Without this the deploy re-reads the unresolved
// origin tip and hits the identical conflict, discarding the finished work
// (SC-2845 — the failure mode that stranded a resolved branch for a human).
func TestAdvanceDeployFix_Done_PublishesResolutionBeforeDeploy(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/13", Number: 13},
		checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", ExitDone))
	assert.Equal(t, []string{"feat/x"}, p.published,
		"the fixer's local resolution must be published to the card's branch")
	assert.Equal(t, 1, p.publishCalls, "the resolution is published exactly once per done exit")
}

// A publish the host refuses (the never-publish-behind-origin guard, an
// unreadable ref) must red the card with the reason and NOT deploy: deploying
// anyway would merge the unresolved origin tip the fixer was dispatched to fix.
// It also pins the ordering — publish happens before the deploy, not after.
func TestAdvanceDeployFix_PublishFails_RedsWithoutDeploying(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	// Wired to deploy cleanly if it were reached — so a regression that deploys
	// past a failed publish fails on the assertions below, not on a panic.
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/13", Number: 13},
		checks:     []forge.ChecksState{forge.ChecksPassing},
		publishErr: errors.New("refusing to publish feat/x: the source is behind origin")}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", ExitDone)
	require.Error(t, err, "an unpublishable resolution is a deploy failure")
	assert.Zero(t, p.call, "a failed publish must not open or re-gate a pull request")
	assert.Zero(t, p.merged, "a failed publish must never reach the merge")

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed, "the card reds when the resolution cannot be published")
	assert.Contains(t, failed, "could not be published to feat/x")
	assert.Contains(t, failed, "behind origin", "the marker carries the host's reason")
}

// Only a done exit has a resolution to carry: a fixer that stopped for a human
// left nothing publishable, and publishing on its behalf would ship whatever
// half-finished state its branch happens to hold.
func TestAdvanceDeployFix_NonDoneExit_PublishesNothing(t *testing.T) {
	for _, exit := range []StageExit{ExitNeedsInput, ExitNeedsHumanWork, ""} {
		c := &fakeCommenter{comments: deployFixReadyComments()}
		p := &fakeDeployer{}
		deps := newDeps(c, &fakeLauncher{}, p)
		require.NoError(t, deps.AdvanceDeployFix(context.Background(), "SC-1", exit))
		assert.Zero(t, p.publishCalls, "exit %q must not publish a branch", exit)
	}
}

// A non-done fixer exit reds the card with an actionable, exit-specific reason.
func TestAdvanceDeployFix_NeedsInput_Reds(t *testing.T) {
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", ExitNeedsInput)
	require.NoError(t, err)
	assert.Zero(t, p.merged, "a needs-input exit never touches the deployer")

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Contains(t, failed, "needs a human decision")
}

// An unrecorded (empty) exit — a crashed fixer — reds via the default escalation
// branch rather than proceeding on a state the driver cannot read (AD5).
func TestAdvanceDeployFix_UnrecordedExit_Reds(t *testing.T) {
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", "")
	require.NoError(t, err)
	assert.Zero(t, p.merged)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Contains(t, failed, "stopped without recovering the deploy")
}

func TestDeployFixRounds_CountsMarkers(t *testing.T) {
	comments := []tracker.Comment{
		cmt("[human:ready-for-review]", time.Unix(1, 0)),
		cmt(DeployFixStartedHeader+"\nround 1", time.Unix(2, 0)),
		cmt("[human:deployed]", time.Unix(3, 0)),
		cmt(DeployFixStartedHeader+"\nround 2", time.Unix(4, 0)),
	}
	assert.Equal(t, 2, deployFixRounds(comments))
	assert.Zero(t, deployFixRounds(nil))
}

func TestCiFailureFixable(t *testing.T) {
	assert.True(t, ciFailureFixable(errors.New("CI checks failed")))
	assert.False(t, ciFailureFixable(errors.New("timed out waiting for CI checks")))
	assert.False(t, ciFailureFixable(nil))
	// An unreadable check state (credential/vault failure) is not a code defect a
	// fixer can repair — it must never dispatch the fixer (SC-1996 AC4).
	assert.False(t, ciFailureFixable(markStateUnreadable(
		errors.New("resolving 1Password secret via CLI: exit status 1"),
		"could not read the pull request's check state")))
}

// stateUnreadable must tag only the read-error, and ciFailureHeadline must keep
// the three outcomes distinct: a credential/vault read failure names an
// unreadable state and the op-signin remedy, a timeout says the CI did not
// finish, and a plain failure tells the user to fix the failing checks (SC-1996
// AC1/AC2/AC3).
func TestStateUnreadable_And_Headlines(t *testing.T) {
	unreadable := markStateUnreadable(
		errors.New("resolving 1Password secret via CLI: exit status 1"),
		"could not read the pull request's check state")
	assert.True(t, stateUnreadable(unreadable))
	assert.False(t, stateUnreadable(errors.New("CI checks failed")))
	assert.False(t, stateUnreadable(errors.New("timed out waiting for CI checks")))

	credential := ciFailureHeadline(unreadable)
	assert.Contains(t, credential, "credential")
	assert.Contains(t, credential, "op signin")
	assert.NotContains(t, credential, "CI checks failed")
	assert.NotContains(t, credential, "fix the failing checks")

	timeout := ciFailureHeadline(errors.New("timed out waiting for CI checks"))
	assert.Contains(t, timeout, "did not finish")

	failing := ciFailureHeadline(errors.New("CI checks failed"))
	assert.Contains(t, failing, "fix the failing checks")
}

// A failing gate names the offending checks: waitForChecks reads the PR's
// per-check state, stamps the failing names as a structured detail, and
// ciFailureHeadline renders them into the next-step headline the fixer reads.
func TestWaitForChecks_failingNamesChecks(t *testing.T) {
	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "build", Conclusion: forge.ChecksFailing},
			{Name: "unit", Conclusion: forge.ChecksPassing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	err := deps.waitForChecks(context.Background(), PRResult{Number: 21, URL: "https://example/pr/21"})
	require.Error(t, err)
	names, _ := humanerrors.AllDetails(err)[deployFailingChecksDetail].(string)
	assert.Equal(t, "build", names)
	assert.Equal(t,
		"CI checks failed on the pull request (failing: build) — fix the failing checks, then re-run Deploy",
		ciFailureHeadline(err))
}

// The names lookup is best-effort: a read failure yields no names, and the
// headline degrades to today's bare reason rather than masking the gate verdict
// (the SC-1996 rule).
func TestWaitForChecks_failingReadErrorDegrades(t *testing.T) {
	p := &fakeDeployer{
		checks:     []forge.ChecksState{forge.ChecksFailing},
		prStateErr: errors.New("read failed"),
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	err := deps.waitForChecks(context.Background(), PRResult{Number: 21, URL: "https://example/pr/21"})
	require.Error(t, err)
	assert.Equal(t,
		"CI checks failed on the pull request — fix the failing checks, then re-run Deploy",
		ciFailureHeadline(err))
}

// The timeout path names the checks still running when the gate gave up.
func TestCiFailureHeadline_timeoutNamesRunning(t *testing.T) {
	err := humanerrors.WithDetails("timed out waiting for CI checks", "pr", "https://example/pr/21",
		deployRunningChecksDetail, "integration")
	assert.Equal(t,
		"CI did not finish within the deploy window (still running: integration) — check the PR's checks, then re-run Deploy",
		ciFailureHeadline(err))
}

// A credential/unreadable-state failure keeps its remedy headline untouched — the
// check-name suffix never attaches to it.
func TestCiFailureHeadline_unchangedForCredential(t *testing.T) {
	unreadable := markStateUnreadable(
		errors.New("resolving 1Password secret via CLI: exit status 1"),
		"could not read the pull request's check state")
	headline := ciFailureHeadline(unreadable)
	assert.Contains(t, headline, "credential")
	assert.NotContains(t, headline, "failing:")
	assert.NotContains(t, headline, "still running:")
}

// A credential/vault read failure at the CI gate must be reported as an
// unreadable state with the op-signin remedy — never as "CI checks failed" — and
// must NOT dispatch the deploy-fixer even with a launcher wired and budget free.
// The PR is fully green; the daemon simply cannot read the token (SC-1996).
func TestDeployBranch_ChecksUnreadable_ReportsCredentialFailure_NoFixer(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/21", Number: 21},
		checksErr: errors.New("resolving 1Password secret via CLI: exit status 1")}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed, started string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
	}
	require.NotEmpty(t, failed, "an unreadable check state must red the card, not spin a fixer")
	assert.Empty(t, started, "no fixer may be dispatched for an unreadable check state")
	assert.Zero(t, l.calls, "an unreadable state is not a code defect — never launch the fixer")
	assert.NotContains(t, failed, "CI checks failed")
	assert.NotContains(t, failed, "fix the failing checks")
	assert.Contains(t, failed, "credential")
	assert.Contains(t, failed, "op signin")
	assert.Contains(t, failed, "resolving 1Password secret", "the raw cause stays in the detail block")
	assert.Zero(t, p.merged, "an unread head must never be merged")
}

// The post-rebase forge fallback reads the forge's mergeability verdict; when
// that read itself fails on a credential/vault error, the deploy must report the
// unreadable state with the op-signin remedy — not the "conflicts with the base"
// conflict headline — and must not dispatch the fixer (SC-1996).
func TestDeployBranch_RebaseFallbackUnreadable_ReportsCredentialFailure_NoFixer(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/22", Number: 22},
		checks:       []forge.ChecksState{forge.ChecksPassing},
		ensureErr:    errors.New("rebase hit a conflict"),
		mergeableErr: errors.New("resolving 1Password secret via CLI: exit status 1")}
	l := &fakeLauncher{}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed, started string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
		if strings.HasPrefix(b, DeployFixStartedHeader) {
			started = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Empty(t, started)
	assert.Zero(t, l.calls, "an unreadable mergeability state must not dispatch a fixer")
	assert.NotContains(t, failed, "conflicts with the base")
	assert.Contains(t, failed, "op signin")
	assert.Zero(t, p.merged)
}

// After the freshness rebase the deploy waits out the forge's mergeability
// recompute; when that read fails terminally on a credential/vault error, the
// failure must name the unreadable state and the op-signin remedy rather than
// claiming the PR is unmergeable (SC-1996).
func TestDeployBranch_AwaitMergeableUnreadable_ReportsCredentialFailure(t *testing.T) {
	syncDeploy(t)
	origInterval, origTimeout := mergeablePollInterval, mergeablePollTimeout
	mergeablePollInterval, mergeablePollTimeout = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { mergeablePollInterval, mergeablePollTimeout = origInterval, origTimeout })

	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/23", Number: 23},
		checks:       []forge.ChecksState{forge.ChecksPassing},
		rebased:      true,
		mergeableErr: errors.New("resolving 1Password secret via CLI: exit status 1")}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	assert.NotContains(t, failed, "unmergeable")
	assert.Contains(t, failed, "op signin")
	assert.Zero(t, p.merged)
}

// The planning dispatch is the pipeline's only entry into building, so the gate
// has to be unskippable there — but it must also not re-review a ticket that
// already carries a verdict, or a planning retry would loop through the gate
// forever.
func TestPlanPromptGatesPlanningOnTicketReview(t *testing.T) {
	p := planPrompt("SC-1")

	assert.Contains(t, p, "/human-ticket-review SC-1", "planning must be gated by the ticket review")
	assert.Contains(t, p, "/human-plan SC-1", "a ready ticket must still reach planning")
	assert.Contains(t, p, "[human:ticket-review]", "the skip condition names the marker to look for")

	// The gate acts on its own findings; a board run has nobody to ask.
	assert.Contains(t, p, "no user to ask")
}

// A terminal verdict stops the run, and a stop the board cannot see is not a
// stop: without the terminal marker the card keeps deriving planning/running and
// the stuck-running pass re-plans it forever (SC-3149, twelve times overnight).
func TestPlanPromptNamesTheTerminalMarkerForUnplannableVerdicts(t *testing.T) {
	p := planPrompt("SC-1")

	assert.Contains(t, p, "superseded, escalated or rejected", "all three terminal verdicts must be covered")
	assert.Contains(t, p, "human marker post SC-1 nothing-to-do",
		"a terminal verdict must post the marker that resolves the planning stage")
	assert.Contains(t, p, "re-plans the ticket forever",
		"the dispatch must say what happens without the marker, so the rule is not dropped as boilerplate")
}
