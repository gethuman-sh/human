package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func optComment(body string, at time.Time) tracker.Comment {
	return tracker.Comment{Body: body, Created: at}
}

const optionsBody = `[human:options]
stage: implementation
context: review found a blocking design gap
1: Add a daemon-side planning re-run path (recommended)
2: Remove the Retry plan menu item and defer criterion 3`

func TestParseOptionsBlock(t *testing.T) {
	stage, context, opts := parseOptionsBlock(optionsBody)
	assert.Equal(t, BoardImplementation, stage)
	assert.Equal(t, "review found a blocking design gap", context)
	require.Len(t, opts, 2)
	assert.Equal(t, "1", opts[0].ID)
	assert.Equal(t, "Add a daemon-side planning re-run path (recommended)", opts[0].Label)
	assert.Equal(t, "2", opts[1].ID)
}

// A malformed block — no stage line, or no options — is not a decision and is
// ignored (empty return). A well-formed block naming a stage the board cannot
// resume is NOT dropped here: it is returned so callers can surface it as a
// visible error rather than letting it vanish (SC-2137).
func TestParseOptionsBlock_MalformedIgnored(t *testing.T) {
	stage, _, opts := parseOptionsBlock("[human:options]\n1: no stage line")
	assert.Equal(t, BoardStage(""), stage)
	assert.Empty(t, opts)

	stage, _, opts = parseOptionsBlock("[human:options]\nstage: implementation")
	assert.Equal(t, BoardStage(""), stage)
	assert.Empty(t, opts)
}

// A well-formed block naming an unrecognized (or non-agent) stage is returned
// as-is — the silent drop that hid the ticket-review gate's decision is gone.
// Resumability is a separate check (optionStages), left to the caller.
func TestParseOptionsBlock_UnknownStageReturned(t *testing.T) {
	stage, _, opts := parseOptionsBlock("[human:options]\nstage: done\n1: ship it")
	assert.Equal(t, BoardDoneStage, stage)
	require.Len(t, opts, 1)
	assert.False(t, optionStages[stage], "done is not an agent-launching stage")
}

// The ticket-review gate names "stage: ticket-review"; the alias resolves it to
// the planning stage so the decision is resumable and renders in the planning
// column the card already occupies while the gate runs (SC-2137).
func TestParseOptionsBlock_TicketReviewAlias(t *testing.T) {
	stage, context, opts := parseOptionsBlock(
		"[human:options]\nstage: ticket-review\ncontext: root or symptom\n1: fix the symptom\n2: fix the cause")
	assert.Equal(t, BoardPlanning, stage)
	assert.True(t, optionStages[stage], "the aliased stage must be resumable")
	assert.Equal(t, "root or symptom", context)
	require.Len(t, opts, 2)
}

// The card carries the latest unconsumed options block: decision needed.
func TestDeriveBoardCard_CarriesOpenOptions(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(ImplementationStartedHeader, base),
		optComment(ReadyForReviewHeader+"\nbranch: b", base.Add(1*time.Minute)),
		optComment(ReviewCompleteHeader+"\nverdict: fail\n\nfindings", base.Add(2*time.Minute)),
		optComment(optionsBody, base.Add(3*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	require.Len(t, card.Options, 2)
	assert.Equal(t, "review found a blocking design gap", card.OptionsContext)
	assert.Equal(t, BoardImplementation, card.OptionsStage)
}

// A chosen option consumes the block: the decision is made, the card must
// not keep asking.
func TestDeriveBoardCard_OptionChosenConsumes(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(optionsBody, base),
		optComment(OptionChosenHeader+" 1: Add a daemon-side planning re-run path", base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Empty(t, card.Options, "a chosen option block must be consumed")
}

// Any later stage-started marker consumes the block too — a pursued (or
// simply superseded) decision disappears from the card.
func TestDeriveBoardCard_StageStartConsumesOptions(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(optionsBody, base),
		optComment(ImplementationStartedHeader, base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Empty(t, card.Options, "a stage start after the block must consume it")
}

func TestApplyOptionPostsChoiceAndRelaunches(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt(ReviewCompleteHeader+"\nverdict: fail", base.Add(1*time.Minute)),
		cmt(optionsBody, base.Add(2*time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "2"})
	require.NoError(t, err)

	require.Len(t, c.added, 2)
	assert.Equal(t, OptionChosenHeader+" 2: Remove the Retry plan menu item and defer criterion 3", c.added[0])
	assert.Equal(t, ImplementationStartedHeader, c.added[1])
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "board-SC-9-implementation", l.name)
	assert.Contains(t, l.prompt, "/human-execute SC-9")
	assert.Contains(t, l.prompt, "Remove the Retry plan menu item")
	assert.Contains(t, l.prompt, OptionChosenHeader)
}

// A block naming the planning stage relaunches the planner, not the executor.
func TestApplyOptionPlanningStage(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:options]\nstage: planning\ncontext: two architectures possible\n1: event-driven\n2: polling", time.Now().Add(-time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"})
	require.NoError(t, err)
	assert.Equal(t, "board-SC-9-planning", l.name)
	assert.Contains(t, l.prompt, "/human-plan SC-9")
	require.Len(t, c.added, 2)
	assert.Equal(t, PlanningStartedHeader, c.added[1])
}

// An unknown option ID must not record a choice or launch anything — the
// grant is exactly what the user saw.
func TestApplyOptionUnknownID(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(optionsBody, time.Now().Add(-time.Minute))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "7"})
	require.Error(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

// A consumed (already chosen) block cannot be chosen again — a double-click
// or stale UI must not dispatch a second run.
func TestApplyOptionConsumedBlockRejected(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(optionsBody, base),
		cmt(OptionChosenHeader+" 1: Add a daemon-side planning re-run path", base.Add(time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"})
	require.Error(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

// SC-1669: a running stage whose own agent stopped on an open same-stage
// [human:options] fork must NOT keep deriving BoardRunning — the card is
// paused waiting for a human, not working. It still carries the options block.
func TestDeriveBoardCard_OpenSameStageOptionsEndsRunning(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, base),
		optComment("[human:options]\nstage: planning\ncontext: pick storage\n1: sqlite\n2: files", base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardPlanning, card.Stage)
	assert.NotEqual(t, BoardRunning, card.State, "an open same-stage decision fork must end the running state")
	assert.Equal(t, BoardIdle, card.State)
	require.Len(t, card.Options, 2, "the open decision block stays attached")
}

// SC-1669 stage-precision companion: an options block naming a stage the card
// has NOT reached does not belong to the running stage and must not clear it.
func TestDeriveBoardCard_OpenOtherStageOptionsKeepsRunning(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, base),
		optComment("[human:options]\nstage: implementation\ncontext: x\n1: a\n2: b", base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardRunning, card.State, "a foreign-stage options block must not clear an active run")
}

// An older options block does not resurface after later pipeline activity;
// only the newest block, and only while unconsumed, is offered.
func TestDeriveBoardCard_LatestBlockWins(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment("[human:options]\nstage: implementation\n1: old choice", base),
		optComment(ImplementationStartedHeader, base.Add(1*time.Minute)),
		optComment(optionsBody, base.Add(2*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	require.Len(t, card.Options, 2)
	assert.Equal(t, "Add a daemon-side planning re-run path (recommended)", card.Options[0].Label)
}

// SC-2137 Fix 1: a decision the ticket-review gate raises (stage: ticket-review)
// reaches the board. The gate runs as the first phase of the planning dispatch,
// so the card sits in Planning running; its decision must attach as a resumable
// planning-stage decision and pause the run rather than being parsed to nothing.
func TestDeriveBoardCard_TicketReviewDecisionReachesBoard(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, base),
		optComment(TicketReviewStartedHeader, base.Add(time.Minute)),
		optComment("[human:options]\nstage: ticket-review\ncontext: root or symptom\n1: fix the symptom now\n2: fix the cause first", base.Add(2*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	require.Len(t, card.Options, 2, "the gate's decision must reach the board, not vanish")
	assert.Equal(t, BoardPlanning, card.OptionsStage, "the ticket-review decision resumes (and renders as) planning")
	assert.Equal(t, "root or symptom", card.OptionsContext)
	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardIdle, card.State, "an open gate decision pauses the run, not runs it")
	assert.Empty(t, card.Error, "a resumable decision is not an error")
}

// SC-2137 Fix 1: choosing a ticket-review decision resumes the gate — it
// relaunches the planning dispatch (which re-runs /human-ticket-review since no
// verdict marker exists yet) with the choice injected.
func TestApplyOptionTicketReviewResumesGate(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanningStartedHeader, base),
		cmt(TicketReviewStartedHeader, base.Add(time.Minute)),
		cmt("[human:options]\nstage: ticket-review\ncontext: root or symptom\n1: fix the symptom\n2: fix the cause", base.Add(2*time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "2"})
	require.NoError(t, err)

	require.Len(t, c.added, 2)
	assert.Equal(t, OptionChosenHeader+" 2: fix the cause", c.added[0])
	assert.Equal(t, PlanningStartedHeader, c.added[1], "resuming the gate re-runs the planning dispatch")
	assert.Equal(t, "board-SC-9-planning", l.name)
	assert.Contains(t, l.prompt, "/human-ticket-review SC-9", "the relaunch re-runs the gate")
	assert.Contains(t, l.prompt, "fix the cause")
}

// SC-2137 Fix 2: a well-formed decision block naming a stage the board cannot
// resume must NOT vanish silently — it surfaces as a visible card error, so a
// future pipeline stage that forgets to register cannot repeat the silent drop.
func TestDeriveBoardCard_UnknownStageOptionsSurfacesError(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, base),
		optComment("[human:options]\nstage: frobnicate\ncontext: x\n1: a\n2: b", base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Empty(t, card.Options, "an unresumable decision offers no clickable options")
	assert.Equal(t, BoardFailed, card.State, "the card must show the error, not carry on silently")
	assert.Contains(t, card.Error, "frobnicate")
	assert.Contains(t, card.Error, "cannot resume")
}

// SC-2137 Fix 2: choosing an unresumable-stage decision is refused loudly rather
// than launching into the wrong (default) stage.
func TestApplyOptionUnresumableStageRejected(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:options]\nstage: frobnicate\ncontext: x\n1: a\n2: b", time.Now().Add(-time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"})
	require.Error(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added, "no choice is recorded for a stage the board cannot resume")
}

// SC-2137 Fix 2: the failure watcher must treat an unresumable-stage block as a
// clean stop — never a *-failed marker and relaunch — so it cannot loop. The
// error is surfaced through derivation instead.
func TestStagePausedOnOptions_UnresumableIsCleanStop(t *testing.T) {
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, time.Now().Add(-time.Hour)),
		optComment("[human:options]\nstage: frobnicate\n1: a", time.Now().Add(-time.Minute)),
	}
	assert.True(t, stagePausedOnOptions(comments, BoardPlanning),
		"an unresumable block must be a clean stop so no failed marker loops")
}

// SC-2137: an option-chosen consuming an unresumable-stage block must not
// synthesize a queued placement — there is no relaunch to represent, so the
// card falls back to its real markers instead of a bogus queued stage.
func TestOptionChosenQueued_UnresumableStageNotQueued(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, base),
		optComment("[human:options]\nstage: frobnicate\n1: a\n2: b", base.Add(time.Minute)),
		optComment(OptionChosenHeader+" 1: a", base.Add(2*time.Minute)),
	}
	_, _, ok := optionChosenQueued(comments)
	assert.False(t, ok, "an unresumable chosen stage has no queued placement to synthesize")
}

// SC-2137: the ticket-review gate's own decision is a genuine pause of the
// planning dispatch, so the failure watcher must not red it.
func TestStagePausedOnOptions_TicketReviewPauses(t *testing.T) {
	comments := []tracker.Comment{
		optComment(PlanningStartedHeader, time.Now().Add(-time.Hour)),
		optComment("[human:options]\nstage: ticket-review\n1: a\n2: b", time.Now().Add(-time.Minute)),
	}
	assert.True(t, stagePausedOnOptions(comments, BoardPlanning))
}
