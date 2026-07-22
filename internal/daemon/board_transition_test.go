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

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
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
	assert.Equal(t, "/human-plan SC-1", l.prompt)
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
	require.Len(t, c.added, 1)
	assert.Equal(t, ImplementationStartedHeader, c.added[0])
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
	assert.Equal(t, "/human-plan SC-1", l.prompt)
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
	require.Len(t, c.added, 1)
	assert.Equal(t, ReviewStartedHeader, c.added[0])
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
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
	assert.Equal(t, "feat/x", p.req.Branch)
	assert.Equal(t, "My feature", p.req.Title)
	assert.Equal(t, 1, p.merged)
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Equal(t, "SC-1", closed)
	assert.Contains(t, c.added, DeployStartedHeader)
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})

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

func TestApplyTransitionDeployWaitsForPendingChecks(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/8", Number: 8},
		checks: []forge.ChecksState{forge.ChecksPending, forge.ChecksPending, forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 3, p.checkCall)
	assert.Equal(t, 1, p.merged)
}

func TestApplyTransitionDeployChecksFail(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/9", Number: 9}, checks: []forge.ChecksState{forge.ChecksFailing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err) // the transition itself succeeded; the failure is a marker
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
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
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
	headline, _, _ := strings.Cut(strings.TrimPrefix(failed, DeployFailedHeader+"\n"), "\n")
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardDoneStage, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err) // async pipeline: the push failure lands as a marker
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
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: fail\n\nmissing error handling", time.Unix(2, 0)),
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
		cmt("[human:review-complete]\nverdict: pass", time.Unix(1, 0)),
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

func TestApplyTransitionDeployAllowedWithPassWithNotes(t *testing.T) {
	syncDeploy(t)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: pass with notes", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/11", Number: 11}, checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.merged)
}

func TestVerdictFailed(t *testing.T) {
	assert.True(t, VerdictFailed("fail"))
	assert.True(t, VerdictFailed("  FAILED — see findings"))
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
	require.Len(t, c.added, 1)
	assert.Equal(t, ImplementationStartedHeader, c.added[0])
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

func (f *gateProbeDeployer) EnsureMergeable(_ context.Context, _ PRRequest) (bool, error) {
	return false, nil
}

func (f *gateProbeDeployer) PullRequestMergeable(_ context.Context, _ string, _ int) (bool, error) {
	return true, nil
}

func (f *gateProbeDeployer) MergePullRequest(_ context.Context, _ string, _ int) error { return nil }

func (f *gateProbeDeployer) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }

func (f *gateProbeDeployer) BranchMerged(_ context.Context, _, _ string) bool { return false }

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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Zero(t, p.merged)
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline, _, _ := strings.Cut(strings.TrimPrefix(failed, DeployFailedHeader+"\n"), "\n")
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Empty(t, p.deleted, "an unmerged branch must not be deleted")
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline, _, _ := strings.Cut(strings.TrimPrefix(failed, DeployFailedHeader+"\n"), "\n")
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
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	headline, _, _ := strings.Cut(strings.TrimPrefix(failed, DeployFailedHeader+"\n"), "\n")
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
	assert.Equal(t, "/human-plan SC-1", l.prompt)
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
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
