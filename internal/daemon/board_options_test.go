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

// A block naming an unknown or non-agent stage must be ignored rather than
// letting a typo relaunch nothing (or the wrong thing).
func TestParseOptionsBlock_InvalidStageIgnored(t *testing.T) {
	stage, _, opts := parseOptionsBlock("[human:options]\nstage: done\n1: ship it")
	assert.Equal(t, BoardStage(""), stage)
	assert.Empty(t, opts)

	stage, _, opts = parseOptionsBlock("[human:options]\n1: no stage line")
	assert.Equal(t, BoardStage(""), stage)
	assert.Empty(t, opts)
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
