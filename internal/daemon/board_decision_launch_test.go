package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A fix run raises its decision in PREFLIGHT — before triage, before any plan.
// Answering it used to hand the resume to the plan executor, which refused the
// launch for having no plan, posted [human:needs-planning] and drove PLANNING on
// a bug ticket: the answer produced the one outcome nobody picked. It must
// resume the pipeline that owns the ticket (the SC-2986 class on the decision
// path).
func TestApplyOption_ImplementationAnswerResumesTheFixPipeline(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt("[human:pipeline]\nkind: fix", base.Add(time.Second)),
		cmt(optionsBody, base.Add(time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "2"}))

	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-autofix SC-9 --board")
	assert.Contains(t, l.prompt, OptionChosenHeader, "the resumed run is pointed at the answer that resumed it")
	for _, added := range c.added {
		assert.NotContains(t, added, NeedsPlanningHeader,
			"a self-planning pipeline must never be asked to go and get a plan")
	}
}

// The security sibling.
func TestApplyOption_ImplementationAnswerResumesTheSecurityPipeline(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt("[human:pipeline]\nkind: security", base.Add(time.Second)),
		cmt(optionsBody, base.Add(time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"}))

	assert.Contains(t, l.prompt, "/human-security-fix SC-9 --board")
}

// A decision on an ordinary plan-executing card is unchanged: the executor runs
// the plan the ticket carries.
func TestApplyOption_ImplementationAnswerOnAPlannedCardStillExecutes(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanReadyHeader, base),
		cmt(ImplementationStartedHeader, base.Add(time.Second)),
		cmt(optionsBody, base.Add(time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"}))

	assert.Contains(t, l.prompt, "/human-execute SC-9")
}

// queuedComments is a card whose decision was answered and whose stage never
// started — what reconcileQueuedLaunch re-drives, and what a person re-dropping
// a held card on its own column moves.
func queuedComments(stage BoardStage, at time.Time) []tracker.Comment {
	return []tracker.Comment{
		cmt(OptionsHeader+"\nstage: "+string(stage)+"\n1: this way\n2: that way", at),
		cmt(OptionChosenHeader+" 1: this way\nstage: "+string(stage), at.Add(time.Minute)),
	}
}

// The recovery pass for a decision whose launch never happened (SC-3865) drives
// the card through ApplyTransition — and no rule there admitted a QUEUED card,
// so every attempt was rejected as a non-advance and charged against the retry
// budget. The pass could not start a single card it existed for.
func TestApplyTransition_QueuedCardLaunchesItsDecidedStage(t *testing.T) {
	for _, tc := range []struct {
		stage  BoardStage
		prompt string
		header string
	}{
		{BoardPlanning, "/human-ticket-review SC-9", PlanningStartedHeader},
		{BoardVerification, "/human-review SC-9", ReviewStartedHeader},
	} {
		base := time.Now().Add(-time.Hour)
		c := &fakeCommenter{comments: queuedComments(tc.stage, base)}
		l := &fakeLauncher{}
		deps := newDeps(c, l, &fakeDeployer{})

		require.NoError(t, deps.ApplyTransition(context.Background(),
			BoardTransitionRequest{PMKey: "SC-9", From: tc.stage, To: tc.stage}))

		assert.Equal(t, 1, l.calls, "%s: the queued stage starts", tc.stage)
		assert.Contains(t, l.prompt, tc.prompt)
		assert.Contains(t, l.prompt, OptionChosenHeader,
			"%s: the launch carries the answer it is carrying out", tc.stage)
		assert.Contains(t, c.added, tc.header)
	}
}

// The same release on a fix card: the pipeline classifier decides, exactly as
// on the click path — and the stale [human:implementation-started] the agent
// that ASKED the question left behind must not swallow the launch, which is
// what ApplyFix's own marker-shaped idempotency guard would have done.
func TestApplyTransition_QueuedFixCardResumesTheFixPipeline(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := append([]tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt("[human:pipeline]\nkind: fix", base.Add(time.Second)),
	}, queuedComments(BoardImplementation, base.Add(time.Minute))...)
	c := &fakeCommenter{comments: comments}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-9", From: BoardImplementation, To: BoardImplementation}))

	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-autofix SC-9 --board")
}

// Dropping a HELD card on its own column is the person's override: they chose
// the order and they may change their mind without unpicking the decision.
func TestApplyTransition_DroppingAHeldCardStartsItAnyway(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(sequencingBody, base),
		cmt(OptionChosenHeader+" 1: SC-4245 goes first\nstage: planning\nwaits-for: SC-4245", base.Add(time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-9", From: BoardPlanning, To: BoardPlanning}))

	assert.Equal(t, 1, l.calls, "a person can always start work the machine is holding")
}

// The daemon may take a sole direction for itself, but never a WAIT: holding a
// ticket behind another one is a sequencing call about someone's backlog, and
// taking it here would park the card on a decision no person ever saw.
func TestPursueSoleDirection_NeverTakesAWait(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(PlanReadyHeader, time.Now().Add(-time.Hour))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.pursueSoleDirection(context.Background(), "SC-9", c.comments,
		BoardOption{ID: "1", Label: "rebuild against the other branch", WaitsFor: "SC-2"}))

	require.NotEmpty(t, c.added)
	assert.NotContains(t, c.added[0], "waits-for:")
	assert.Equal(t, 1, l.calls, "the direction is still pursued")
}
