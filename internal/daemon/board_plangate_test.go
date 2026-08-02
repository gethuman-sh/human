package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// planGateDeps builds a transition whose card is ready to move into
// implementation, carrying whatever comments the case needs.
func planGateDeps(t *testing.T, comments []tracker.Comment) (BoardTransitionDeps, *fakeCommenter, *fakeLauncher) {
	t.Helper()
	c := &fakeCommenter{comments: comments}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	return deps, c, l
}

func moveToImplementation(deps BoardTransitionDeps) error {
	return deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
}

// The route the reported failure came in by: a decision answered on an
// unplanned ticket relaunched implementation, which then found nothing to
// execute — and repeated that on every answer. Planning is what the ticket
// needs, so the machine goes there instead of dispatching the executor again.
func TestApplyOption_noPlanRunsPlanningInsteadOfTheExecutor(t *testing.T) {
	deps, _, l := planGateDeps(t, []tracker.Comment{
		cmt("[human:options]\nstage: implementation\ncontext: which way\n1: this way\n2: that way", time.Now().Add(-time.Minute)),
	})

	require.NoError(t, deps.ApplyOption(context.Background(),
		BoardOptionRequest{PMKey: "SC-1", OptionID: "1"}))

	require.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-plan", "an unplanned ticket is planned, not executed")
	assert.NotContains(t, l.prompt, "/human-execute")
}

// The same decision on a planned ticket still resumes the executor, carrying
// the choice — the guard must not swallow the ordinary path.
func TestApplyOption_withAPlanStillResumesTheExecutor(t *testing.T) {
	deps, _, l := planGateDeps(t, []tracker.Comment{
		cmt("[human:plan]\nstep one", time.Now().Add(-2*time.Minute)),
		cmt("[human:options]\nstage: implementation\ncontext: which way\n1: this way\n2: that way", time.Now().Add(-time.Minute)),
	})

	require.NoError(t, deps.ApplyOption(context.Background(),
		BoardOptionRequest{PMKey: "SC-1", OptionID: "1"}))

	assert.Contains(t, l.prompt, "/human-execute")
	assert.Contains(t, l.prompt, "decision was made", "the choice must reach the run")
}

// The guard belongs to the executor, not the stage: the bug pipeline launches
// this same stage with no plan on purpose, and a guard on the stage would
// refuse every autofix run.
func TestApplyFix_stillRunsWithoutAPlan(t *testing.T) {
	deps, _, l := planGateDeps(t, []tracker.Comment{cmt("[human:bug-verdict] head=confirmed", time.Now().Add(-time.Minute))})

	require.NoError(t, deps.ApplyFix(context.Background(), BoardFixRequest{PMKey: "SC-1", PMTitle: "a bug"}))

	require.Equal(t, 1, l.calls, "autofix must not be gated on a plan it produces itself")
	assert.Contains(t, l.prompt, "/human-autofix")
}

// hasPlan reads the whole thread, not just the latest comment.
func TestHasPlan_findsAPlanAnywhereInTheThread(t *testing.T) {
	comments := []tracker.Comment{
		cmt("[human:plan]\nstep one", time.Now().Add(-time.Hour)),
		cmt("[human:implementation-failed]", time.Now()),
	}

	assert.True(t, hasPlan(comments))
	assert.False(t, hasPlan([]tracker.Comment{cmt("[human:claim]", time.Now())}))
}

// "[human:plan]" must not be satisfied by an unrelated marker that merely
// starts with the same letters.
func TestHasPlan_planningStartedIsNotAPlan(t *testing.T) {
	assert.False(t, hasPlan([]tracker.Comment{cmt("[human:planning-started]", time.Now())}))
}
