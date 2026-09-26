package daemon

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// reviewableDeps is the daemon's wiring: a launcher exists, so the route can
// enter the machine review. The bare CLI has none — that case is pinned apart.
func reviewableDeps(c *fakeCommenter, p *fakeDeployer) (BoardTransitionDeps, *fakeLauncher) {
	l := &fakeLauncher{}
	return newDeps(c, l, p), l
}

// bareDeps is the wiring a run with no daemon builds: no launcher at all, not a
// nil pointer inside the interface, which is what newDeps(c, nil, p) produces.
func bareDeps(c *fakeCommenter, p *fakeDeployer) BoardTransitionDeps {
	return BoardTransitionDeps{Commenter: c, Deployer: p, WorkspaceDir: "/ws", ConfigDir: "/ws"}
}

func runStartDeploy(t *testing.T, deps BoardTransitionDeps, req StartDeployRequest) (StartDeployResult, error) {
	t.Helper()
	return deps.StartDeploy(context.Background(), req)
}

// Could not READ the state being gated on — an ordinary failure, not a
// refusal and not a reason to run the engine.
func TestStartDeploy_listCommentsErrorStopsBeforeTheEngine(t *testing.T) {
	c := listErrCommenter{&fakeCommenter{}}
	p := &fakeDeployer{}
	deps, _ := reviewableDeps(&fakeCommenter{}, p)
	deps.Commenter = c

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	assert.False(t, stderrors.Is(err, ErrDeployAwaitingDecision), "a load failure is not the awaiting-decision refusal")
	assert.Zero(t, p.call, "the engine must never run when the guard could not be evaluated")
}

// The refusal is the deploy's one non-failure outcome: nothing may be shipped
// while a person's own open question sits unanswered on the ticket, and
// refusing must not look like the deploy broke.
func TestStartDeploy_refusalIsNotAFailure(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1"},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", ID: "2"},
	}}
	p := &fakeDeployer{}
	deps, _ := reviewableDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployAwaitingDecision))
	assert.Empty(t, c.added, "a refusal is not a failure: it posts no marker at all")
	assert.Zero(t, p.call, "the engine (fakeDeployer's PushAndCreatePR) must never run on a refusal")
}

// The CLI route enters the machine: the start is recorded, the branch pushed
// with its PR in DRAFT, and the reviewer launched — nothing merges yet. This
// is the F10 fix: SC-4406 reached main through `human deploy` with no
// reviewer ever having read it.
func TestStartDeploy_recordsTheStartThenEntersTheReviewLoop(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{res: PRResult{Number: 42, URL: "https://example/pr/42", Draft: true}}
	deps, l := reviewableDeps(c, p)

	res, err := runStartDeploy(t, deps, StartDeployRequest{
		PMKey: "SC-1", Title: "t", PRBody: "body", Branch: "feat/x",
	})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeReviewStarted, res.Outcome)
	assert.Equal(t, "https://example/pr/42", res.PRURL)
	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], DeployStartedHeader)
	assert.Contains(t, c.added[0], "branch: feat/x")
	assert.NotContains(t, c.added[0], "override:", "a plain deploy records no override")
	assert.Equal(t, 1, p.call, "PushAndCreatePR must run exactly once, after the start marker")
	assert.True(t, p.req.Draft, "the PR must open in draft so a half-reviewed change cannot merge")
	assert.Equal(t, "board-SC-1-prreview", l.name)
	assert.Equal(t, "/human-pr-review SC-1 --pr=42 --branch=feat/x", l.prompt)
	started, ok := posted(c, PRReviewStartedHeader)
	require.True(t, ok, "the loop's own start marker must be posted so its Stop-hook driver can find the PR")
	assert.Contains(t, started, "number: 42")
	assert.Zero(t, p.merged, "nothing merges before the review approves")
	assert.Zero(t, p.markReadyCall, "nothing un-drafts before the review approves")
}

// The base advanced past the freshly-opened branch and the merge conflicts:
// the deploy fixer is dispatched BEFORE any reviewer runs, and the caller
// must be told a fixer — not a reviewer — owns the step (SC-5279). Reporting
// DeployOutcomeReviewStarted here would tell `human deploy`'s caller a
// reviewer is working when a fixer is.
func TestStartDeploy_staleBaseConflict_reportsFixDispatchedNotReviewStarted(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{res: PRResult{Number: 42, URL: "https://example/pr/42", Draft: true}, freshness: FreshnessConflict}
	deps, l := reviewableDeps(c, p)

	res, err := runStartDeploy(t, deps, StartDeployRequest{
		PMKey: "SC-1", Title: "t", PRBody: "body", Branch: "feat/x",
	})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeFixDispatched, res.Outcome)
	assert.Equal(t, "https://example/pr/42", res.PRURL)
	_, reviewStarted := posted(c, PRReviewStartedHeader)
	assert.False(t, reviewStarted, "no review round starts on a conflicting branch")
	started, ok := posted(c, DeployFixStartedHeader)
	require.True(t, ok, "the deploy fixer is dispatched instead")
	assert.Contains(t, started, "before: review")
	assert.Equal(t, "/human-deploy-fix SC-1 --pr=42 --branch=feat/x", l.prompt)
}

// A blocking verification verdict is refused before any push, with no marker:
// the ticket already says what has to happen. The board's own route has
// refused this since the verdict gate existed; the CLI route walked past it.
func TestStartDeploy_blockingVerdictRefusesBeforePush(t *testing.T) {
	base := time.Now()
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1", Created: base},
		{Body: "[human:review-complete]\nverdict: fail", ID: "2", Created: base.Add(time.Minute)},
	}}
	p := &fakeDeployer{}
	deps, l := reviewableDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployVerdictBlocks))
	assert.Empty(t, c.added, "a refusal posts no marker")
	assert.Zero(t, p.call, "nothing is pushed on a blocking verdict")
	assert.Zero(t, l.calls)
}

// A verdict judges the round it was posted against: a newer handoff retires it
// (SC-4958), so the rebuilt work is reviewed rather than refused on the old
// verdict.
func TestStartDeploy_verdictRetiredByALaterHandoffProceeds(t *testing.T) {
	base := time.Now()
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1", Created: base},
		{Body: "[human:review-complete]\nverdict: fail", ID: "2", Created: base.Add(time.Minute)},
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "3", Created: base.Add(2 * time.Minute)},
	}}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u", Draft: true}}
	deps, l := reviewableDeps(c, p)

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeReviewStarted, res.Outcome)
	assert.Equal(t, 1, l.calls)
}

// A process with no launcher cannot run the review the merge depends on, and
// must not pretend it did: refused, no marker, nothing pushed. This is the
// bare CLI with no daemon.
func TestStartDeploy_noLauncherRefusesWithoutReady(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{}
	deps := bareDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployReviewUnavailable))
	assert.Empty(t, c.added)
	assert.Zero(t, p.call)
}

// --ready is the person's override of the review interlock: it runs the engine
// directly, needs no launcher, and the start marker says the review was
// skipped — an unrecorded override would read as a reviewed merge.
func TestStartDeploy_readyShipsWithoutTheReviewAndRecordsIt(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{
		res:       PRResult{Number: 42, URL: "https://example/pr/42"},
		checks:    []forge.ChecksState{forge.ChecksPassing},
		mergeable: true,
	}
	deps, l := reviewableDeps(c, p)
	deps.MergeDraftPR = true

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Title: "t", PRBody: "b", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeShipped, res.Outcome)
	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], "override: shipped with --ready")
	assert.Zero(t, l.calls, "--ready runs no reviewer")
	assert.False(t, p.req.Draft, "--ready ships a mergeable PR")
	assert.Equal(t, 1, p.merged)
}

// --ready without a launcher is the bare CLI shipping by hand: allowed, because
// it is a person's explicit call, and still recorded.
func TestStartDeploy_readyNeedsNoLauncher(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{alreadyMerged: true}
	deps := bareDeps(c, p)
	deps.MergeDraftPR = true

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeShipped, res.Outcome)
	assert.Contains(t, c.added[0], "override: shipped with --ready")
}

// A still-current approval of exactly this head is reused: the draft is
// un-drafted and the engine runs, exactly as the loop's own merge step does.
// No second review round is spent on work the reviewer already judged.
func TestStartDeploy_reusesAnApprovalBoundToThisHead(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	base := time.Now()
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:pr-review-started]\npr: u\nnumber: 42\nbranch: feat/x", ID: "1", Created: base},
		{Body: prReviewPassedBody("feat/x", head), ID: "2", Created: base.Add(time.Minute)},
		{Body: "[human:deploy-failed]\nreason: CI checks failed", ID: "3", Created: base.Add(2 * time.Minute)},
	}}
	p := &fakeDeployer{
		res:       PRResult{Number: 42, URL: "u", Draft: true},
		prState:   &forge.PullRequestState{Number: 42, HeadSHA: head},
		checks:    []forge.ChecksState{forge.ChecksPassing},
		mergeable: true,
	}
	deps, l := reviewableDeps(c, p)

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Title: "t", PRBody: "b", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeShipped, res.Outcome)
	assert.Zero(t, l.calls, "an approval of this exact head needs no new review")
	assert.Equal(t, 42, p.markedReady, "the draft is released on the strength of the bound approval")
	assert.Equal(t, 1, p.merged)
}

// Approval is evidence about one revision. Anything that fails to bind it to
// the head being shipped runs the review: a later round, a different head, a
// PR the forge cannot report, or an older marker that recorded no head.
func TestStartDeploy_unboundApprovalRunsTheReview(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	base := time.Now()
	approved := tracker.Comment{Body: prReviewPassedBody("feat/x", head), ID: "2", Created: base.Add(time.Minute)}
	for _, tc := range []struct {
		name     string
		comments []tracker.Comment
		deployer *fakeDeployer
	}{
		{"a later review round supersedes it",
			[]tracker.Comment{approved, {Body: "[human:pr-review-started]\npr: u\nnumber: 42\nbranch: feat/x", ID: "3", Created: base.Add(2 * time.Minute)}},
			&fakeDeployer{res: PRResult{Number: 42, URL: "u", Draft: true}, prState: &forge.PullRequestState{HeadSHA: head}}},
		{"a later handoff supersedes it",
			[]tracker.Comment{approved, {Body: "[human:ready-for-review]\nbranch: feat/x", ID: "3", Created: base.Add(2 * time.Minute)}},
			&fakeDeployer{res: PRResult{Number: 42, URL: "u", Draft: true}, prState: &forge.PullRequestState{HeadSHA: head}}},
		{"the branch moved past the approved head",
			[]tracker.Comment{approved},
			&fakeDeployer{res: PRResult{Number: 42, URL: "u", Draft: true}, prState: &forge.PullRequestState{HeadSHA: "fedcba9876543210fedcba9876543210fedcba98"}}},
		{"the approval names another branch",
			[]tracker.Comment{{Body: prReviewPassedBody("feat/other", head), ID: "2", Created: base.Add(time.Minute)}},
			&fakeDeployer{res: PRResult{Number: 42, URL: "u", Draft: true}, prState: &forge.PullRequestState{HeadSHA: head}}},
		{"the forge cannot report the head",
			[]tracker.Comment{approved},
			&fakeDeployer{res: PRResult{Number: 42, URL: "u", Draft: true}, prStateErr: stderrors.New("forge unreachable")}},
		{"an older approval recorded no head",
			[]tracker.Comment{{Body: PRReviewPassedHeader, ID: "2", Created: base.Add(time.Minute)}},
			&fakeDeployer{res: PRResult{Number: 42, URL: "u", Draft: true}, prState: &forge.PullRequestState{HeadSHA: head}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCommenter{comments: tc.comments}
			deps, l := reviewableDeps(c, tc.deployer)

			res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

			require.NoError(t, err)
			assert.Equal(t, DeployOutcomeReviewStarted, res.Outcome)
			assert.Equal(t, 1, l.calls, "the reviewer must run")
			assert.Zero(t, tc.deployer.merged)
			assert.Zero(t, tc.deployer.markReadyCall, "the draft stays until the loop approves")
		})
	}
}

// Already-merged work has nothing to review: the engine's carve-out records the
// terminal marker and closes the ticket, and no PR or reviewer is started.
func TestStartDeploy_alreadyMergedIsShippedWithoutAReview(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{alreadyMerged: true}
	deps, l := reviewableDeps(c, p)
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeShipped, res.Outcome)
	assert.Zero(t, p.call)
	assert.Zero(t, l.calls)
	assert.Equal(t, "SC-1", closed)
	_, ok := posted(c, DeployedHeader)
	assert.True(t, ok)
}

// A push that fails is the deploy failing, on the ticket and to the caller.
func TestStartDeploy_pushFailureIsADeployFailure(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{prErr: stderrors.New("remote rejected")}
	deps, l := reviewableDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	failed, ok := posted(c, DeployFailedHeader)
	require.True(t, ok)
	assert.Contains(t, failed, "could not push feat/x")
	assert.Zero(t, l.calls)
}

// A missing comment post is a lost sentence, not a reason to withhold the
// ship: the merge is the work. Applies to the plain start record only — an
// override that cannot be recorded is a different case, pinned below.
func TestStartDeploy_aFailedMarkerPostStillShips(t *testing.T) {
	c := &fakeCommenter{addErr: stderrors.New("tracker unavailable")}
	p := &fakeDeployer{alreadyMerged: true}
	deps, _ := reviewableDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{
		PMKey: "SC-1", Title: "t", PRBody: "body", Branch: "feat/x",
	})

	assert.NoError(t, err)
}

// The override line is the only record that a ship walked past a guard.
// Nothing has been pushed at that point, so a lost post there must stop the
// deploy rather than ship an unrecorded override — the one case where the
// record IS the point, unlike the best-effort plain start marker. Both
// overrides are pinned: the decision one and the review one.
func TestStartDeploy_aFailedOverrideRecordStopsTheShip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		comments []tracker.Comment
		ready    bool
		override bool
	}{
		{"open decision", []tracker.Comment{
			{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1"},
			{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", ID: "2"},
		}, false, true},
		{"--ready", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCommenter{comments: tc.comments, addErr: stderrors.New("tracker unavailable")}
			p := &fakeDeployer{alreadyMerged: true}
			deps, _ := reviewableDeps(c, p)
			deps.MergeDraftPR = tc.ready

			_, err := runStartDeploy(t, deps, StartDeployRequest{
				PMKey: "SC-1", Branch: "feat/x", OverrideDecision: tc.override,
			})

			require.Error(t, err)
			assert.Zero(t, p.call, "the engine must never run when the override record could not be posted")
		})
	}
}

// --override-decision is a person deciding to ship past their own open
// question — it must still record the start and still run the engine.
func TestStartDeploy_overrideShipsPastAnOpenDecision(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1"},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", ID: "2"},
	}}
	p := &fakeDeployer{alreadyMerged: true}
	deps, _ := reviewableDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{
		PMKey: "SC-1", Branch: "feat/x", OverrideDecision: true,
	})

	require.NoError(t, err)
	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], DeployStartedHeader)
	assert.Contains(t, c.added[0], "override: deployed with an open decision on stage implementation — c",
		"an override that leaves no trace is indistinguishable from never having been asked")
}

// Pins gap 1 shut: DeployStartedHeader has exactly one production poster
// (StartDeploy), so a future refactor that removes it silently is caught here
// rather than by a missing ticket comment nobody notices.
func TestDeployStartedHeader_hasAProductionPoster(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{alreadyMerged: true}
	deps, _ := reviewableDeps(c, p)

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})
	require.NoError(t, err)

	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], DeployStartedHeader)
}

// The loop's converging marker binds what it approved: branch and head. A
// later `human deploy` reuses it only for that head, so the record has to
// carry it.
func TestAdvancePRLoop_approvalRecordsTheReviewedHead(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	// The thread predates the loop's own post, which the fake stamps with now.
	base := time.Now().Add(-time.Minute)
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "2", Created: base.Add(time.Second)},
	}}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, checks: []forge.ChecksState{forge.ChecksPassing}, mergeable: true}
	deps, _ := reviewableDeps(c, p)

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewHead: head}))

	passed, ok := posted(c, PRReviewPassedHeader)
	require.True(t, ok)
	assert.Contains(t, passed, "branch: feat/x")
	assert.Contains(t, passed, "head: "+head)
	got, bound := currentApproval(c.comments, "feat/x")
	assert.True(t, bound)
	assert.Equal(t, head, got)
}

// The launch gate is asked before anything is recorded or pushed: a host that
// cannot launch the reviewer refuses like the other two refusals — no marker,
// nothing moved — instead of pushing, printing "review started" and leaving
// the card in deploying with nothing behind it (SC-5108).
func TestStartDeploy_gateRefusedReviewerRefusesBeforeAnythingIsRecorded(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{res: PRResult{Number: 42, URL: "https://example/pr/42", Draft: true}}
	deps, l := reviewableDeps(c, p)
	deps.LaunchGate = func(context.Context) []DoctorCheck {
		return []DoctorCheck{{ID: "claude-auth", Name: "Claude authentication", OK: false, Detail: "login wiped"}}
	}

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployReviewUnavailable))
	assert.Empty(t, c.added, "a refusal posts no marker")
	assert.Zero(t, p.call, "nothing is pushed")
	assert.Zero(t, l.calls)
}

// --ready needs no reviewer, so the gate does not apply to it.
func TestStartDeploy_readyIgnoresTheReviewerGate(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{alreadyMerged: true}
	deps, _ := reviewableDeps(c, p)
	deps.MergeDraftPR = true
	deps.LaunchGate = func(context.Context) []DoctorCheck {
		return []DoctorCheck{{ID: "claude-auth", Name: "Claude authentication", OK: false, Detail: "login wiped"}}
	}

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeShipped, res.Outcome)
}

// A reviewer already owning the step on this machine is a review that is,
// truthfully, started: its marker stands and its exit drives the loop.
func TestStartDeploy_reviewerAlreadyRunningIsAStartedReview(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{res: PRResult{Number: 42, URL: "https://example/pr/42", Draft: true}}
	deps, l := reviewableDeps(c, p)
	l.err = ErrAgentAlreadyRunning

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeReviewStarted, res.Outcome)
	_, failed := posted(c, DeployFailedHeader)
	assert.False(t, failed, "an owned step is not a failure")
}

// The deploy fixer is only the remedy for a deploy that already failed. When
// the gate refuses the remedy, the failure itself is recorded — not swallowed
// as a fixer "already running" (SC-5108, round 4).
func TestDispatchDeployFixer_gateRefusalRecordsTheDeployFailure(t *testing.T) {
	c := &fakeCommenter{}
	deps, l := reviewableDeps(c, &fakeDeployer{})
	deps.LaunchGate = func(context.Context) []DoctorCheck {
		return []DoctorCheck{{ID: "claude-auth", Name: "Claude authentication", OK: false, Detail: "login wiped"}}
	}

	err := deps.dispatchDeployFixer(context.Background(), "SC-1", PRResult{Number: 7, URL: "u"}, "feat/x", "CI checks failed on the pull request", false, 0)

	require.Error(t, err)
	assert.Zero(t, l.calls)
	failed, ok := posted(c, DeployFailedHeader)
	require.True(t, ok, "the deploy failure must be on the ticket")
	assert.Contains(t, failed, "CI checks failed")
	_, fixStarted := posted(c, DeployFixStartedHeader)
	assert.False(t, fixStarted)
}

// The CLI route refuses the checkout interlock exactly like the board route
// waits for it — but here it reports the refusal to the caller instead of
// abandoning silently, since a person or an agent is waiting for an answer.
func TestStartDeploy_refusesWhileTheImplementationContainerHoldsTheCheckout(t *testing.T) {
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	p := &fakeDeployer{}
	deps, l := reviewableDeps(c, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "autofix/sc-1"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployCheckoutBusy))
	assert.Contains(t, err.Error(), "deploy refused")
	assert.Zero(t, p.call, "nothing is pushed into a checkout another stage holds")
	assert.Zero(t, l.calls)
	for _, b := range c.added {
		assert.NotContains(t, b, DeployStartedHeader, "a deploy that never started may not record a start")
		assert.NotContains(t, b, DeployFailedHeader, "a refusal is not a failure")
	}
}

// AD3: the CLI's `human deploy` route posts NOTHING when it refuses on the
// checkout interlock — no started marker, no failure, and no queued record
// either. Unlike a board drop (which accepts and queues behind the checkout,
// SC-5878), the CLI route has no reconcile pass watching over an in-process
// wait, so recording a queue it can never withdraw would strand the card
// exactly the way a lost daemon restart would. human-autofix-skill.md and
// human-security-fix-skill.md both restate "nothing failed, no marker is
// posted, and the card is not red" for this route, and the two assertions
// above alone would still pass if the route grew a queued marker: this pins
// silence, not just the absence of the two other headers.
func TestStartDeploy_CheckoutBusyStillPostsNothing(t *testing.T) {
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	p := &fakeDeployer{}
	deps, l := reviewableDeps(c, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "autofix/sc-1"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployCheckoutBusy))
	assert.Zero(t, p.call)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added, "the CLI route must post nothing at all on this refusal, not merely omit the started/failed headers")
}

// --ready overrides the machine review, not the checkout: the engine it runs
// writes to the same tree the implementation container holds.
func TestStartDeploy_readyDoesNotOverrideTheCheckoutInterlock(t *testing.T) {
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	p := &fakeDeployer{}
	deps, _ := reviewableDeps(c, p)
	deps.LiveAgents = liveAgents("board-SC-1-implementation")
	deps.MergeDraftPR = true

	_, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "autofix/sc-1"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployCheckoutBusy))
	assert.Zero(t, p.call)
}

// Once the implementation container ends, the wait resolves and the deploy
// proceeds — the wait happens before the start marker, so a refused deploy
// never records one.
func TestStartDeploy_proceedsOnceTheContainerIsGone(t *testing.T) {
	shortCheckoutWait(t)
	c := &fakeCommenter{comments: reviewedThread()}
	p := &fakeDeployer{res: PRResult{Number: 42, URL: "https://example/pr/42", Draft: true}}
	deps, _ := reviewableDeps(c, p)
	calls := 0
	deps.LiveAgents = func() ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"board-SC-1-implementation"}, nil
		}
		return nil, nil
	}

	res, err := runStartDeploy(t, deps, StartDeployRequest{PMKey: "SC-1", Branch: "autofix/sc-1"})

	require.NoError(t, err)
	assert.Equal(t, DeployOutcomeReviewStarted, res.Outcome)
	_, found := posted(c, DeployStartedHeader)
	assert.True(t, found, "the wait happens before the start marker, so a deploy that proceeds must still record its start")
}
