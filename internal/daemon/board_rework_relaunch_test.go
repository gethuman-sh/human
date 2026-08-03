package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A card the self-planning fix pipeline built (posts [human:bug-verdict], attaches
// no [human:plan]) whose review then FAILS must, on the rework re-drop, restart the
// FIX pipeline — not the plan executor, whose plan gate would refuse it for having
// no plan (SC-2989 sibling of SC-2986).
func TestReworkReDispatchesFixPipeline_bugVerdictNoPlan(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:bug-verdict] confirmed\n\nroot cause: X", time.Unix(1, 0)),
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(2, 0)),
		cmt("[human:review-complete]\nverdict: fail\n\nmissing error handling", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Bug"}}

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.NoError(t, err)

	assert.Contains(t, l.prompt, "/human-autofix", "a fix-built card reworks as the fix pipeline")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader,
			"a fix-pipeline rework must never ask a human to run planning")
	}
}

// Fallback with no Getter wired: the marker heuristic (bug-verdict + no plan) still
// routes the rework to the fix pipeline.
func TestReworkFixPipelineFallbackNoGetter(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:bug-verdict] confirmed", time.Unix(1, 0)),
		cmt("[human:review-complete]\nverdict: fail\n\nmissing X", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{}) // Getter nil
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-autofix")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader)
	}
}

// A plan-executing (non-fix) build reworked after a failed review is UNCHANGED: it
// carries a plan, so it re-dispatches the executor with the review-findings pointer.
func TestReworkLeavesPlanExecutorUntouched(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(2, 0)),
		cmt("[human:review-complete]\nverdict: fail\n\nmissing X", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Feature"}}
	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-execute SC-1")
	assert.Contains(t, l.prompt, "review found problems")
	assert.NotContains(t, l.prompt, "/human-autofix")
}
