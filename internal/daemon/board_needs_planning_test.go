package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// The implementation stage exists to carry out a plan, so its launch must be
// refused on a ticket that has none — checked at the single launch chokepoint so
// every route in is covered, not only the drag gesture (SC-2596).

func TestHasPlanEvidence(t *testing.T) {
	t.Run("plan comment (single-tracker)", func(t *testing.T) {
		assert.True(t, hasPlanEvidence([]tracker.Comment{
			cmt("[human:plan]\n## Changes\n- do the thing", time.Unix(1, 0)),
		}))
	})
	t.Run("plan-ready marker (both topologies)", func(t *testing.T) {
		assert.True(t, hasPlanEvidence([]tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0)),
		}))
	})
	t.Run("no plan at all", func(t *testing.T) {
		assert.False(t, hasPlanEvidence([]tracker.Comment{
			cmt("[human:implementation-started]", time.Unix(1, 0)),
		}))
	})
	t.Run("planning-started alone is not a plan", func(t *testing.T) {
		// A planning run that never reached plan-ready has produced no plan to
		// execute — the implementation stage must still refuse.
		assert.False(t, hasPlanEvidence([]tracker.Comment{
			cmt("[human:planning-started]", time.Unix(1, 0)),
		}))
	})
}

// A [human:needs-planning] refusal, being a planning-stage marker, must win over
// the phantom implementation markers it refused (which outrank planning), so the
// card returns to Planning carrying the determination — never a running build
// the stuck-running reconcile would later red as a crash.
func TestDeriveBoardCard_NeedsPlanningOverPhantomImplementation(t *testing.T) {
	comments := []tracker.Comment{
		cmt("[human:implementation-started]", time.Unix(1, 0)),
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(2, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardFailed, card.State)
	// The planning gesture must be available so a human can trigger the plan.
	assert.True(t, isPlanningRetry(BoardPlanning, card),
		"a needs-planning card must qualify for the planning relaunch gesture")
	assert.Contains(t, card.Error, "no plan")
}

// After a human triggers planning from a needs-planning card and it completes,
// the fresh plan-ready is the newest marker, so the override stands down and the
// card reads Planning/Done — ready to implement. Because the guard refused before
// any implementation-started was posted, no phantom implementation marker fights
// furthest-stage-wins here.
func TestDeriveBoardCard_NeedsPlanningSupersededByLaterPlanning(t *testing.T) {
	comments := []tracker.Comment{
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(1, 0)),
		cmt("[human:planning-started]", time.Unix(2, 0)),
		cmt("[human:plan-ready]", time.Unix(3, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardDone, card.State, "planning has completed; the card is ready to implement")
}

// The build-retry route (a re-drive of an implementation card) reaches
// startAgentStage without a drag gesture — exactly the class of route the
// gesture gate never covered. With no plan it must refuse — and, since the
// SC-2990 fix, drive the card into planning rather than parking it silently.
func TestBuildRetryRefusedWithoutPlan(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:implementation-failed]\nagent died", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls, "the refusal drives the card into planning")
	assert.Contains(t, l.prompt, "/human-plan SC-1")
	require.Len(t, c.added, 2)
	assert.Contains(t, c.added[0], NeedsPlanningHeader)
	assert.Contains(t, c.added[1], PlanningStartedHeader)
	assert.NotContains(t, c.added, ImplementationStartedHeader)
}

func TestBuildRetryAllowedWithPlanReady(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, c.added, ImplementationStartedHeader)
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader)
	}
}

func TestBuildRetryAllowedWithPlanComment(t *testing.T) {
	// Single-tracker topology: the plan lives as a [human:plan] comment, no
	// separate engineering ticket and no plan-ready engineering line.
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan]\n## Changes\n- do the thing", time.Unix(1, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
}

// The self-contained fix pipelines produce their plan within the run, so they
// legitimately launch the implementation stage with no pre-written plan.
func TestAutofixLaunchesWithoutPlan(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-1"})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls, "autofix is not plan-gated")
	assert.Contains(t, l.prompt, "/human-autofix")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader)
	}
}

func TestSecurityFixLaunchesWithoutPlan(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplySecurityFix(context.Background(), SecurityFixRequest{PMKey: "SC-1"})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls, "security-fix is not plan-gated")
	assert.Contains(t, l.prompt, "/human-security-fix")
}

// A reconcile re-drive that re-hits the guard must not spam the thread with a
// second needs-planning marker once the determination is already surfaced —
// but an un-acted, not-yet-driven refusal is still driven into planning: the
// card must not park silently just because it already said why (SC-2990).
func TestNeedsPlanningNotRepostedButDrivenWhenAlreadySurfaced(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:implementation-failed]\nagent died", time.Unix(1, 0)),
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader, "the refusal is already on the card; do not re-post it")
	}
	assert.Equal(t, 1, l.calls, "an un-driven refusal must still be driven into planning")
	assert.Contains(t, l.prompt, "/human-plan SC-1")
}

// A tracker blip while re-reading comments after posting the initial refusal
// must not stop the drive into planning from being attempted at all — covered
// implicitly by the direct refuseIfUnplanned call above, which uses the same
// fakeCommenter for both the refusal read and the drive.

// TestNeedsPlanningNotRepostedWhenLaterFailureLands is the regression test for
// defect B: the old guard keyed its dedup on the newest marker OVERALL
// (newestNeedsPlanning), so a later *-failed marker on another stage defeated
// it and the refusal was re-posted once per attempt. The fix keys the dedup on
// the newest PLANNING-STAGE marker instead (latestStateInStage(comments,
// BoardPlanning)), which a later implementation-stage marker cannot displace.
func TestNeedsPlanningNotRepostedWhenLaterFailureLands(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(2, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	needsPlanningPosts := 0
	for _, a := range c.added {
		if strings.HasPrefix(strings.TrimSpace(a), NeedsPlanningHeader) {
			needsPlanningPosts++
		}
	}
	assert.Zero(t, needsPlanningPosts,
		"a later *-failed marker on another stage must not defeat the needs-planning dedup guard")
}

// A comment-read failure is not an absence of plan: a tracker blip must not
// refuse a launch, so the guard proceeds and leaves the agent's own plan check
// as the backstop.
func TestRefuseUnplannedProceedsOnReadError(t *testing.T) {
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, &fakeDeployer{})
	deps.Commenter = listErrCommenter{&fakeCommenter{}}
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.False(t, refused, "a read blip must not be treated as a missing plan")
}

// The gate is scoped to plan-executing implementation launches: it is a no-op for
// other stages and for launches that declared no plan requirement.
func TestRefuseUnplannedScope(t *testing.T) {
	c := &fakeCommenter{}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	t.Run("non-implementation stage", func(t *testing.T) {
		refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardPlanning, true)
		require.NoError(t, err)
		assert.False(t, refused)
	})
	t.Run("requiresPlan false", func(t *testing.T) {
		refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, false)
		require.NoError(t, err)
		assert.False(t, refused)
	})
	assert.Empty(t, c.added, "a no-op gate reads no comments and posts nothing")
}

// A failure posting the needs-planning marker is reported (refused, error) so the
// launch is still withheld — an unplanned ticket must never start, even when the
// surfacing comment could not be written.
func TestRefuseUnplannedReportsPostError(t *testing.T) {
	c := &fakeCommenter{addErr: errors.New("comment api down")}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.Error(t, err)
	assert.True(t, refused)
}

// A fresh refusal on a ticket with no planning-stage history at all must be
// surfaced AND driven into planning — a person must never have to notice the
// refusal and trigger planning by hand (SC-2990).
func TestUnplannedDrivenIntoPlanning(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:implementation-failed]\nagent died", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	require.Len(t, c.added, 2)
	assert.Contains(t, c.added[0], NeedsPlanningHeader)
	assert.Contains(t, c.added[1], PlanningStartedHeader)
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-plan SC-1")
}

// While a driven planning run is still going, a re-hit of the guard must stay
// completely quiet: no second refusal, no second planner launched.
func TestUnplannedQuietWhilePlanningRuns(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(1, 0)),
		cmt(PlanningStartedHeader, time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	assert.Empty(t, c.added)
	assert.Zero(t, l.calls)
}

// Once the ping-pong bound (drive → plan → fail, repeated) is spent, the next
// refusal must escalate to a person exactly once instead of driving another
// doomed planning attempt — the bound that keeps the ticket from ping-ponging
// between planning and implementation forever.
func TestUnplannedEscalatesAfterBound(t *testing.T) {
	origBound := PlanRedriveBound
	PlanRedriveBound = 2
	t.Cleanup(func() { PlanRedriveBound = origBound })

	c := &fakeCommenter{comments: []tracker.Comment{
		// Cycle 1: refuse, drive, planning fails.
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(1, 0)),
		cmt(PlanningStartedHeader, time.Unix(2, 0)),
		cmt(PlanningFailedHeader+"\nno-op", time.Unix(3, 0)),
		// Cycle 2: refuse, drive, planning fails.
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(4, 0)),
		cmt(PlanningStartedHeader, time.Unix(5, 0)),
		cmt(PlanningFailedHeader+"\nno-op", time.Unix(6, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], NeedsPlanningHeader)
	assert.Contains(t, c.added[0], planStuckMarker)
	assert.Contains(t, c.added[0], "2 time(s)")
	assert.Zero(t, l.calls, "a bound-exhausted escalation drives nothing")
}

// The plan-stuck escalation is said once: a re-hit of the guard while it is
// already the newest planning-stage marker must add nothing and launch
// nothing.
func TestUnplannedEscalationSaidOnce(t *testing.T) {
	origBound := PlanRedriveBound
	PlanRedriveBound = 2
	t.Cleanup(func() { PlanRedriveBound = origBound })

	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(1, 0)),
		cmt(PlanningStartedHeader, time.Unix(2, 0)),
		cmt(PlanningFailedHeader+"\nno-op", time.Unix(3, 0)),
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(4, 0)),
		cmt(PlanningStartedHeader, time.Unix(5, 0)),
		cmt(PlanningFailedHeader+"\nno-op", time.Unix(6, 0)),
		cmt(NeedsPlanningHeader+"\n"+planStuckReason(2, cmt(NeedsPlanningHeader, time.Unix(1, 0))), time.Unix(7, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	assert.Empty(t, c.added)
	assert.Zero(t, l.calls)
}
