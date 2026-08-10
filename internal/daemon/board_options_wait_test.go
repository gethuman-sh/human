package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// The fork this exists for: preflight found other open work aimed at the same
// code and asked which goes first.
const sequencingBody = `[human:options]
stage: implementation
context: SC-4245 has an open branch on the same files
1: SC-4245 goes first
waits-for-1: SC-4245
2: this goes first`

// The wait rides with the answer it belongs to, and is never offered as an
// answer of its own — it shares the `id: label` shape of one, so a parser that
// did not reserve it would put a third button on the card.
func TestParseOptionsBlock_WaitBelongsToItsAnswer(t *testing.T) {
	stage, _, opts := parseOptionsBlock(sequencingBody)

	assert.Equal(t, BoardImplementation, stage)
	require.Len(t, opts, 2, "the wait is metadata about answer 1, not a third answer")
	assert.Equal(t, "SC-4245", opts[0].WaitsFor)
	assert.Empty(t, opts[1].WaitsFor, "the other direction defers to nothing")
}

// A wait naming no offered answer is dropped rather than kept as a phantom.
// Posting rejects it, so one reaching here predates the check.
func TestParseOptionsBlock_WaitForAnUnofferedAnswerDropped(t *testing.T) {
	_, _, opts := parseOptionsBlock(
		"[human:options]\nstage: planning\n1: a\n2: b\nwaits-for-3: SC-1")

	require.Len(t, opts, 2)
	for _, o := range opts {
		assert.Empty(t, o.WaitsFor)
	}
}

// The defect this whole change exists to close: picking "<KEY> goes first"
// used to start the very work it deferred, because every answer took the one
// path the machine had. The answer is recorded — with what it waits for — and
// NOTHING is launched.
func TestApplyOption_SequencingAnswerHoldsTheWork(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(ImplementationStartedHeader, time.Now().Add(-time.Hour)),
		cmt(sequencingBody, time.Now().Add(-time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"}))

	require.Len(t, c.added, 1, "the choice is recorded and no stage is started")
	assert.Equal(t, OptionChosenHeader+" 1: SC-4245 goes first\nstage: implementation\nwaits-for: SC-4245", c.added[0],
		"the record carries the wait, so every later reader learns it from the ticket")
	assert.Zero(t, l.calls, "the answer was to go second; starting the work is doing the opposite of it")
}

// The other answer on the same block is unchanged: it starts the stage.
func TestApplyOption_OtherAnswerOnASequencingBlockStillLaunches(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt(PlanReadyHeader, time.Now().Add(-2*time.Hour)),
		cmt(ImplementationStartedHeader, time.Now().Add(-time.Hour)),
		cmt(sequencingBody, time.Now().Add(-time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "2"}))

	assert.Equal(t, 1, l.calls)
	assert.NotContains(t, c.added[0], "waits-for:", "an ordinary answer defers to nothing")
}

// A ticket cannot wait for itself: nothing could ever clear it. The block stays
// open — the stage asked a question that cannot be answered, and treating the
// answer as an ordinary one would start the work it was picked to defer.
func TestApplyOption_SelfWaitRefused(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:options]\nstage: planning\n1: wait\nwaits-for-1: SC-9\n2: go", time.Now().Add(-time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	err := deps.ApplyOption(context.Background(), BoardOptionRequest{PMKey: "SC-9", OptionID: "1"})

	require.Error(t, err)
	assert.Empty(t, c.added, "nothing is recorded, so the block is still there to answer")
	assert.Zero(t, l.calls)
}

// The card has to SAY it is holding, or a card doing exactly what it was told
// is indistinguishable from one nobody picked up.
func TestDeriveBoardCard_HeldCardNamesWhatItWaitsFor(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	card := DeriveBoardCard([]tracker.Comment{
		optComment(ImplementationStartedHeader, base),
		optComment(sequencingBody, base.Add(time.Minute)),
		optComment(OptionChosenHeader+" 1: SC-4245 goes first\nstage: implementation\nwaits-for: SC-4245", base.Add(2*time.Minute)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardQueued, card.State)
	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, "SC-4245", card.WaitsFor)
}

// The hold ends with the record that holds it: once the released stage posts
// its started marker, the card is running and waits for nothing.
func TestDeriveBoardCard_HoldEndsWhenTheStageStarts(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	card := DeriveBoardCard([]tracker.Comment{
		optComment(sequencingBody, base),
		optComment(OptionChosenHeader+" 1: SC-4245 goes first\nstage: implementation\nwaits-for: SC-4245", base.Add(time.Minute)),
		optComment(ImplementationStartedHeader, base.Add(2*time.Minute)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardRunning, card.State)
	assert.Empty(t, card.WaitsFor)
}

// An ordinary answer queues the stage for a launch, not for other work — the
// card must not claim to be waiting for a ticket.
func TestDeriveBoardCard_OrdinaryQueuedCardWaitsForNothing(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	card := DeriveBoardCard([]tracker.Comment{
		optComment(optionsBody, base),
		optComment(OptionChosenHeader+" 2: Remove the Retry plan menu item\nstage: implementation", base.Add(time.Minute)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardQueued, card.State)
	assert.Empty(t, card.WaitsFor)
}

// A held card has no agent to be missing: the viewer must not paint "agent not
// running" over work the machine was told to hold.
func TestAgentNamesForCard_HeldCardNamesNoAgent(t *testing.T) {
	assert.Nil(t, AgentNamesForCard(BoardViewCard{
		Key: "SC-9", Stage: string(BoardImplementation), State: string(BoardQueued), WaitsFor: "SC-4245"}))
	assert.NotEmpty(t, AgentNamesForCard(BoardViewCard{
		Key: "SC-9", Stage: string(BoardImplementation), State: string(BoardQueued)}),
		"an ordinary queued card is still waiting for an agent that should exist")
}

// A block composed in Go renders the wait beside the answer it belongs to, so
// the block that comes back off the ticket parses to the same answers that went
// in. Without it a sequencing answer would post as an ordinary direction.
func TestOptionsMarker_RendersTheWaitBesideItsAnswer(t *testing.T) {
	m, order := optionsMarker(BoardImplementation, "which goes first",
		[]BoardOption{{ID: "1", Label: "SC-4245 goes first", WaitsFor: "SC-4245"}, {ID: "2", Label: "this goes first"}})

	assert.Equal(t, []string{"stage", "context", "1", "waits-for-1", "2"}, order)
	_, _, opts := parseOptionsBlock(markerBody(m))
	require.Len(t, opts, 2, "the wait must not read back as a third answer")
	assert.Equal(t, "SC-4245", opts[0].WaitsFor)
}
