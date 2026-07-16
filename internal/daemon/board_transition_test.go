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

// fakeCommenter records AddComment bodies and returns canned ListComments.
type fakeCommenter struct {
	comments []tracker.Comment
	added    []string
	addErr   error
}

func (f *fakeCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	return f.comments, nil
}

func (f *fakeCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.added = append(f.added, body)
	c := tracker.Comment{Body: body, Created: time.Now()}
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
	return f.checks[i], nil
}

func (f *fakeDeployer) MergePullRequest(_ context.Context, _ string, _ int) error {
	f.merged++
	return f.mergeErr
}

func (f *fakeDeployer) DeleteRemoteBranch(_ context.Context, _, branch string) error {
	f.deleted = append(f.deleted, branch)
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
	assert.Equal(t, "/human-execute HUM-9", l.prompt)
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
	assert.Equal(t, "/human-review HUM-9", l.prompt)
	assert.Contains(t, c.added, ReviewStartedHeader)
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
	assert.Equal(t, "/human-execute SC-1", l.prompt)
}

func TestApplyTransitionVerificationWithoutEngineeringKey(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})
	require.NoError(t, err)
	assert.Equal(t, "/human-review SC-1", l.prompt)
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
