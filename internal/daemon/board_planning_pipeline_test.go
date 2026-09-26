package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// sc4244History is SC-4244's recorded marker sequence, oldest first, truncated
// at the planning-failed that the recovery sweep re-drove. Taken from
// `human marker list SC-4244`: an autofix run asked a question, the answered
// decision was handed to the plan executor, the plan gate refused it for having
// no plan and drove the bug into feature planning.
func sc4244History(base time.Time) []tracker.Comment {
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	return []tracker.Comment{
		cmt(ImplementationStartedHeader, at(1)),
		cmt(PipelineStartedHeader+"\nkind: fix", at(2)),
		cmt(OptionsHeader+"\nstage: implementation\ncontext: two live PRs\n1: this way\n2: that way", at(3)),
		cmt(OptionChosenHeader+" 2: that way\nstage: implementation", at(4)),
		cmt(NeedsPlanningHeader+"\n"+needsPlanningReason, at(5)),
		cmt(PlanningStartedHeader, at(6)),
		cmt(TicketReviewStartedHeader, at(7)),
		cmt(TicketReviewedHeader+"\nverdict: ready", at(8)),
		cmt(ImplementationFailedHeader+"\nreason: agent exited without completing the stage", at(9)),
		cmt(PlanningFailedHeader+"\nreason: agent exited without completing the stage", at(10)),
	}
}

// SC-5793: the recovery sweep re-drives a failed planning stage through
// ApplyRetryTransition (cmd/cmddaemon/daemon.go:882 issues From: stage, To: stage).
// On SC-4244's own history that relaunched the FEATURE planner on a bug — and the
// chain that put the bug there had already asked a person to run planning.
func TestPlanningRetry_SC4244HistoryResumesAutofix(t *testing.T) {
	base := time.Now().Add(-24 * time.Hour)
	c := &fakeCommenter{comments: sc4244History(base)}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{}) // Getter nil: the [human:pipeline] marker is authoritative

	card := DeriveBoardCard(c.comments, tracker.CategoryUnstarted, false)
	require.Equal(t, BoardPlanning, card.Stage, "precondition: the recorded history leaves the card in planning")
	require.Equal(t, BoardFailed, card.State, "precondition: planning failed, which is what the sweep re-drives")

	launched, err := deps.ApplyRetryTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-4244", From: BoardPlanning, To: BoardPlanning})

	require.NoError(t, err)
	assert.True(t, launched, "a resumed fix pipeline is a launch, so the retry may charge for it")
	assert.Equal(t, 1, l.calls)
	assert.Contains(t, l.prompt, "/human-autofix SC-4244 --board")
	assert.NotContains(t, l.prompt, "/human-plan", "a bug is never handed to the feature planner")
	assert.Contains(t, c.added, ImplementationStartedHeader)
	assert.NotContains(t, c.added, PlanningStartedHeader)
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader,
			"the pipeline that writes its own plan must never ask a person to run planning")
	}
}

// The retry charge and the placement: the sweep's own policy, wired to the real
// transition. A resumed fix is a launch (charged once, never refunded) and the
// card must end up where the work actually is.
func TestPlanningRetry_BugChargesOneAttemptAndLandsInImplementation(t *testing.T) {
	base := time.Now().Add(-24 * time.Hour)
	c := &fakeCommenter{comments: sc4244History(base)}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	attempts, uncounts := 0, 0
	policy := StageRetry{
		Max:      DefaultStageRetries,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false }, // a vanished agent
		Attempts: func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Reset:    func(string, BoardStage) {},
		Uncount:  func(string, BoardStage) { uncounts++; attempts-- },
		Relaunch: func(key string, stage BoardStage) (bool, error) {
			return deps.ApplyRetryTransition(context.Background(),
				BoardTransitionRequest{PMKey: key, From: stage, To: stage})
		},
	}

	handled := policy.tryRelaunch(context.Background(), "SC-4244", BoardPlanning, c.comments, c, "d1", zerolog.Nop())

	require.True(t, handled)
	assert.Equal(t, 1, attempts, "a resumed fix pipeline is charged exactly one attempt")
	assert.Zero(t, uncounts, "it launched, so nothing is refunded")
	assert.Contains(t, l.prompt, "/human-autofix")
	var note string
	for _, a := range c.added {
		if strings.HasPrefix(a, "Automatic retry ") {
			note = a
		}
	}
	assert.Equal(t, "Automatic retry 1/2 of the planning stage — the agent exited without recording an outcome.", note)

	after := DeriveBoardCard(c.comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardImplementation, after.Stage, "the card follows the work into the fix stage")
	assert.Equal(t, BoardRunning, after.State)
}

// The security sibling and the feature control, on the same path.
func TestPlanningRetry_ClassifiesByPipeline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		issue        *tracker.Issue
		wantPrompt   string
		wantHeader   string
		unwantPrompt string
	}{
		{"security", &tracker.Issue{Type: "Security", Labels: []string{tracker.SecurityLabel}},
			"/human-security-fix SC-1 --board", ImplementationStartedHeader, "/human-plan"},
		{"feature", &tracker.Issue{Type: "Feature"},
			"/human-plan SC-1", PlanningStartedHeader, "/human-autofix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCommenter{comments: []tracker.Comment{
				cmt(PlanningStartedHeader, time.Unix(1, 0)),
				cmt(PlanningFailedHeader+"\nreason: agent exited without completing the stage", time.Unix(2, 0)),
			}}
			l := &fakeLauncher{}
			deps := newDeps(c, l, &fakeDeployer{})
			deps.Getter = &fakeGetter{issue: tc.issue}

			require.NoError(t, deps.ApplyTransition(context.Background(),
				BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardPlanning}))

			assert.Equal(t, 1, l.calls)
			assert.Contains(t, l.prompt, tc.wantPrompt)
			assert.NotContains(t, l.prompt, tc.unwantPrompt)
			assert.Contains(t, c.added, tc.wantHeader)
		})
	}
}

// The plan gate (SC-4244's origin): a fix ticket is resumed, never refused into
// planning, and the marker that asked a person to run planning is never posted.
func TestPlanGate_BugResumesTheFixPipelineAndPostsNoNeedsPlanning(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(ImplementationFailedHeader+"\nreason: agent died", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Bug"}}

	refused, launched, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)

	require.NoError(t, err)
	assert.True(t, refused, "the plan-executing launch is still withheld")
	assert.True(t, launched, "but the ticket's own pipeline started, so the retry may charge for it")
	assert.Contains(t, l.prompt, "/human-autofix SC-1 --board")
	for _, a := range c.added {
		assert.NotContains(t, a, NeedsPlanningHeader)
		assert.NotContains(t, a, PlanningStartedHeader)
	}
}

// A planner already running on the fix ticket: withhold rather than put a second
// agent on the same ticket. Nothing is posted and nothing is launched.
func TestPlanGate_BugWithAPlannerRunningWithholds(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PipelineStartedHeader+"\nkind: fix", time.Unix(1, 0)),
		cmt(PlanningStartedHeader, time.Unix(2, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	refused, launched, err := deps.refuseIfUnplanned(context.Background(), "SC-1", BoardImplementation, true)

	require.NoError(t, err)
	assert.True(t, refused)
	assert.False(t, launched)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

// Re-open after a nothing-to-do a mis-dispatched planner reached on a bug.
func TestReopenResolved_BugResumesTheFixPipeline(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PipelineStartedHeader+"\nkind: fix", time.Unix(1, 0)),
		cmt(PlanningStartedHeader, time.Unix(2, 0)),
		cmt(NothingToDoHeader+"\nevidence: nothing to plan\nreason: rejected", time.Unix(3, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardPlanning, Reopen: true}))

	assert.Contains(t, l.prompt, "/human-autofix SC-1 --board")
	assert.Contains(t, l.prompt, "re-examine it rather than repeating the earlier conclusion")
	assert.Contains(t, c.added, ImplementationStartedHeader)
}

// The forward drop onto Planning.
func TestForwardDropOnPlanning_BugResumesTheFixPipeline(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Getter = &fakeGetter{issue: &tracker.Issue{Type: "Bug"}}

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardPlanning}))

	assert.Contains(t, l.prompt, "/human-autofix SC-1 --board")
	assert.Contains(t, c.added, ImplementationStartedHeader)
	assert.NotContains(t, c.added, PlanningStartedHeader)
}

// A decision answered while the card sits in planning.
func TestDecidedPlanningStage_BugResumesTheFixPipelineWithTheDirection(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := append([]tracker.Comment{
		cmt(PipelineStartedHeader+"\nkind: fix", base),
	}, queuedComments(BoardPlanning, base.Add(time.Minute))...)
	c := &fakeCommenter{comments: comments}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-9", From: BoardPlanning, To: BoardPlanning}))

	assert.Contains(t, l.prompt, "/human-autofix SC-9 --board")
	assert.Contains(t, l.prompt, OptionChosenHeader, "the resumed run carries the answer it is carrying out")
}
