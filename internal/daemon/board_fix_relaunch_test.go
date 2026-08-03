package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// fakeGetter scripts the PM issue a relaunch reads to classify the pipeline.
type fakeGetter struct {
	issue *tracker.Issue
	err   error
}

func (f *fakeGetter) GetIssue(_ context.Context, _ string) (*tracker.Issue, error) {
	return f.issue, f.err
}

// A fix run interrupted after triage posted its verdict but before a plan was
// attached must, on recovery relaunch, re-dispatch the autofix pipeline (which
// produces its own plan) rather than the plan-executing build-retry path — and
// must never post [human:needs-planning], the command it is itself running (SC-2986).
func TestFixRelaunchReDispatchesAutofix_bugVerdictNoPlan(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:bug-verdict] confirmed\n\nroot cause: X", time.Unix(1, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Bug"}}

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)

	assert.Equal(t, 1, l.calls, "the fix pipeline is re-dispatched")
	assert.Contains(t, l.prompt, "/human-autofix")
	assert.Contains(t, l.prompt, "--board")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader,
			"a self-planning fix relaunch must never ask a human to run planning")
	}
	assert.Contains(t, c.added, ImplementationStartedHeader)
}

// The security-fix sibling: a security ticket relaunches through
// /human-security-fix, never the plan gate.
func TestFixRelaunchReDispatchesSecurityFix(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:bug-verdict] confirmed", time.Unix(1, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Security", Labels: []string{tracker.SecurityLabel}}}

	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-security-fix")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader)
	}
}

// Fallback with no Getter wired: a [human:bug-verdict] trail and no plan is
// treated as an autofix relaunch rather than falling to the plan gate.
func TestFixRelaunchFallbackBugVerdictNoGetter(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:bug-verdict] confirmed", time.Unix(1, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{}) // Getter is nil
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Contains(t, l.prompt, "/human-autofix")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader)
	}
}

// A non-fix (feature) ticket relaunched in implementation is UNCHANGED: no
// verdict, a plan present -> the plan-executing build retry still runs, and a
// bug-verdict never appears so classification returns "none".
func TestFixRelaunchLeavesPlanExecutingBuildUntouched(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:plan-ready]", time.Unix(1, 0)),
		cmt("[human:implementation-failed]\nagent died", time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Feature"}}
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{
		PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.NotContains(t, l.prompt, "/human-autofix")
}
