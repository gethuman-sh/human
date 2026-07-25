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

// loopDeps builds transition deps with the review→fix loop enabled and a fixed
// loop-state snapshot injected — the shape the daemon board wiring produces.
func loopDeps(c *fakeCommenter, l *fakeLauncher, p *fakeDeployer, snap PRLoopStateSnapshot) BoardTransitionDeps {
	deps := newDeps(c, l, p)
	deps.PRReviewLoop = true
	deps.PRLoopState = func(string) PRLoopStateSnapshot { return snap }
	return deps
}

func TestStartPRLoopAgentPostsMarkerAndLaunches(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.startPRLoopAgent(context.Background(), "SC-1", PRReviewAgent, PRReviewStartedHeader,
		"/human-pr-review SC-1 --pr=7 --branch=feat/x", "https://example/pr/7", "feat/x")
	require.NoError(t, err)
	// The started marker carries the pr: and branch: lines the exit handler
	// recovers the loop's PR from.
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], PRReviewStartedHeader))
	assert.Contains(t, c.added[0], "pr: https://example/pr/7")
	assert.Contains(t, c.added[0], "branch: feat/x")
	// The launched agent name routes its Stop back into the loop.
	assert.Equal(t, "board-SC-1-pr-review", l.name)
}

func TestStartPRLoopAgentLaunchFailsPostsFailed(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: errors.New("docker down")}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.startPRLoopAgent(context.Background(), "SC-1", PRReviewAgent, PRReviewStartedHeader,
		"/human-pr-review SC-1 --pr=7 --branch=feat/x", "https://example/pr/7", "feat/x")
	require.Error(t, err)
	// The done stage reds with a pr-review-failed marker rather than a stuck spinner.
	var failed bool
	for _, b := range c.added {
		if strings.HasPrefix(b, PRReviewFailedHeader) {
			failed = true
		}
	}
	assert.True(t, failed, "a launch failure must post a pr-review-failed marker")
}

func TestHandlePRLoopExitReviewApprovedMarksReadyAndMerges(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(PRReviewStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
	}}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}, checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := loopDeps(c, &fakeLauncher{}, p, PRLoopStateSnapshot{ReviewVerdict: PRVerdictApproved})
	origInterval := deployCheckInterval
	deployCheckInterval = time.Millisecond
	t.Cleanup(func() { deployCheckInterval = origInterval })
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }
	require.NoError(t, deps.HandlePRLoopExit(context.Background(), "SC-1"))
	assert.Equal(t, 1, p.readied, "the approved PR must be marked ready before merge")
	assert.Equal(t, 1, p.merged)
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Equal(t, "SC-1", closed)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/7")
}

func TestHandlePRLoopExitChangesRequestedLaunchesFixer(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(PRReviewStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := loopDeps(c, l, &fakeDeployer{}, PRLoopStateSnapshot{ReviewVerdict: PRVerdictChanges})
	require.NoError(t, deps.HandlePRLoopExit(context.Background(), "SC-1"))
	assert.Equal(t, "board-SC-1-pr-fix", l.name, "changes-requested below budget launches the fixer")
	var started bool
	for _, b := range c.added {
		if strings.HasPrefix(b, PRFixStartedHeader) {
			started = true
		}
	}
	assert.True(t, started, "a pr-fix-started marker must be posted")
}

func TestHandlePRLoopExitFixDoneLaunchesReview(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(PRReviewStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
		cmt(PRFixStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := loopDeps(c, l, &fakeDeployer{}, PRLoopStateSnapshot{FixExit: PRFixDone})
	require.NoError(t, deps.HandlePRLoopExit(context.Background(), "SC-1"))
	assert.Equal(t, "board-SC-1-pr-review", l.name, "a done fix re-runs the reviewer")
}

func TestHandlePRLoopExitBudgetSpentReds(t *testing.T) {
	base := time.Unix(1, 0)
	comments := []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", base)}
	// DefaultPRReviewRounds review-started markers exhaust the budget.
	for i := 0; i < DefaultPRReviewRounds; i++ {
		comments = append(comments,
			cmt(PRReviewStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", base.Add(time.Duration(i+1)*time.Minute)))
	}
	c := &fakeCommenter{comments: comments}
	l := &fakeLauncher{}
	deps := loopDeps(c, l, &fakeDeployer{}, PRLoopStateSnapshot{ReviewVerdict: PRVerdictChanges})
	require.NoError(t, deps.HandlePRLoopExit(context.Background(), "SC-1"))
	assert.Zero(t, l.calls, "a spent budget must not launch another fixer")
	var reason string
	for _, b := range c.added {
		if strings.HasPrefix(b, PRReviewFailedHeader) {
			reason = b
		}
	}
	require.NotEmpty(t, reason, "a spent budget must red the done stage")
	assert.Contains(t, reason, "rounds")
}

func TestHandlePRLoopExitUnreviewableReds(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
		cmt(PRReviewStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
	}}
	deps := loopDeps(c, &fakeLauncher{}, &fakeDeployer{}, PRLoopStateSnapshot{ReviewVerdict: PRVerdictUnreviewable})
	require.NoError(t, deps.HandlePRLoopExit(context.Background(), "SC-1"))
	var reason string
	for _, b := range c.added {
		if strings.HasPrefix(b, PRReviewFailedHeader) {
			reason = b
		}
	}
	require.NotEmpty(t, reason)
	assert.Contains(t, reason, "unreviewable")
}

func TestEscalatePRLoopNeedsInputPostsOptions(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PRFixStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
	}}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	snap := PRLoopStateSnapshot{
		FixExit:    ExitNeedsInput,
		FixSummary: "the header format is ambiguous",
		FixOptions: []BoardOption{{ID: "1", Label: "Direction A"}, {ID: "2", Label: "Direction B"}},
	}
	require.NoError(t, deps.escalatePRLoop(context.Background(), "SC-1", c.comments, snap))
	require.Len(t, c.added, 1)
	block := c.added[0]
	stage, ctxLine, opts := parseOptionsBlock(block)
	assert.Equal(t, BoardImplementation, stage)
	assert.Equal(t, "the header format is ambiguous", ctxLine)
	require.Len(t, opts, 2)
	assert.Equal(t, "Direction A", opts[0].Label)
	assert.Equal(t, "Direction B", opts[1].Label)
}

func TestEscalatePRLoopNeedsInputFallbackSingleOption(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PRFixStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
	}}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	snap := PRLoopStateSnapshot{FixExit: ExitNeedsInput}
	require.NoError(t, deps.escalatePRLoop(context.Background(), "SC-1", c.comments, snap))
	require.Len(t, c.added, 1)
	// The generic fallback keeps the block valid — it must parse with ≥1 option.
	stage, _, opts := parseOptionsBlock(c.added[0])
	assert.Equal(t, BoardImplementation, stage)
	require.Len(t, opts, 1)
}

func TestEscalatePRLoopIdempotentWithOpenBlock(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PRFixStartedHeader+"\npr: https://example/pr/7\nbranch: feat/x", time.Unix(2, 0)),
		cmt(OptionsHeader+"\nstage: implementation\ncontext: earlier\n1: Rebuild", time.Unix(3, 0)),
	}}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	snap := PRLoopStateSnapshot{FixExit: ExitNeedsInput}
	require.NoError(t, deps.escalatePRLoop(context.Background(), "SC-1", c.comments, snap))
	assert.Empty(t, c.added, "an already-open decision block must not be re-posted")
}

func TestDeployBranchLoopOpensDraft(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{URL: "https://example/pr/7", Number: 7}}
	deps := loopDeps(c, l, p, PRLoopStateSnapshot{})
	require.NoError(t, deps.DeployBranch(context.Background(), "SC-1", "My feature", "body", "feat/x"))
	assert.True(t, p.req.Draft, "the loop must open the PR as a draft")
	assert.Equal(t, "board-SC-1-pr-review", l.name, "the first review round must launch")
	assert.Zero(t, p.merged, "the loop must not merge on draft-open")
	assert.Zero(t, p.readied, "the loop must not mark ready on draft-open")
}

// TestMergeTailBehaviorPreserved drives mergeTail directly (the extracted body
// the legacy DeployBranch runs) and asserts the exact pre-split sequence: CI
// gate → merge → delete branch → deployed marker → close.
func TestMergeTailBehaviorPreserved(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{checks: []forge.ChecksState{forge.ChecksPassing}}
	deps := newDeps(c, &fakeLauncher{}, p)
	origInterval := deployCheckInterval
	deployCheckInterval = time.Millisecond
	t.Cleanup(func() { deployCheckInterval = origInterval })
	var closed string
	deps.CloseTicket = func(pmKey string) error { closed = pmKey; return nil }
	err := deps.mergeTail(context.Background(), "SC-1", PRResult{URL: "https://example/pr/7", Number: 7}, "feat/x")
	require.NoError(t, err)
	assert.Equal(t, 1, p.merged)
	assert.Equal(t, []string{"feat/x"}, p.deleted)
	assert.Equal(t, "SC-1", closed)
	assert.Contains(t, c.added, DeployedHeader+"\npr: https://example/pr/7")
}

func TestOpenDraftPRPushFailsReds(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	p := &fakeDeployer{prErr: errors.New("push rejected")}
	deps := loopDeps(c, l, p, PRLoopStateSnapshot{})
	err := deps.DeployBranch(context.Background(), "SC-1", "My feature", "body", "feat/x")
	require.Error(t, err)
	assert.Zero(t, l.calls, "a failed draft-open must not launch a reviewer")
	var failed bool
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = true
			assert.Contains(t, b, "draft pull request")
		}
	}
	assert.True(t, failed, "a failed draft-open must red the done stage")
}

func TestStartPRLoopAgentSkipsOnLaunchGate(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	// A launch-critical doctor check failing means this daemon leaves the work
	// for a healthy one: no marker posted, no launch, silent skip (not an error).
	deps.LaunchGate = func(context.Context) []DoctorCheck {
		return []DoctorCheck{{ID: "docker", Name: "Docker", OK: false}}
	}
	err := deps.startPRLoopAgent(context.Background(), "SC-1", PRReviewAgent, PRReviewStartedHeader,
		"/human-pr-review SC-1 --pr=7 --branch=feat/x", "https://example/pr/7", "feat/x")
	require.NoError(t, err)
	assert.Zero(t, l.calls, "a failing launch gate must not launch")
	assert.Empty(t, c.added, "a failing launch gate must post no marker (leaves work unclaimed)")
}
