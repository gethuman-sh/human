package daemon

import (
	"context"
	"errors"
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
// gesture gate never covered. With no plan it must refuse and surface, not launch.
func TestBuildRetryRefusedWithoutPlan(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:implementation-failed]\nagent died", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Zero(t, l.calls, "no agent is launched for an unplanned ticket")
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], NeedsPlanningHeader)
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
// second needs-planning marker once the determination is already surfaced.
func TestNeedsPlanningNotRepostedWhenAlreadySurfaced(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:implementation-failed]\nagent died", time.Unix(1, 0)),
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	refused, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)
	require.NoError(t, err)
	assert.True(t, refused)
	assert.Empty(t, c.added, "the refusal is already on the card; do not re-post it")
	assert.Zero(t, l.calls)
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
