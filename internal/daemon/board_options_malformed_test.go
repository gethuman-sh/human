package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// sc2405Block is the decision block that shipped on SC-2405, verbatim: a refused
// stage lease turned into a question for a human, offering one real answer, with
// the daemon provenance stamp sitting underneath it in the same `id: label`
// shape a real answer has.
const sc2405Block = "[human:options]\n" +
	"stage: implementation\n" +
	"context: Could not acquire the fix-stage lease for SC-2405 (PR 345) — another agent " +
	"(board-SC-2405-prreview) currently holds it and its heartbeat is live within TTL.\n" +
	"\n" +
	"1: Rebuild the branch to resolve the decision the fixer raised\n" +
	"daemon: 4f3add9a"

// The daemon stamp is provenance, not an answer. It was rendered as a second
// selectable option on the card, so a reader could "decide" the daemon id and
// resume the pipeline on a choice that means nothing.
func TestParseOptionsBlockDropsDaemonStamp(t *testing.T) {
	_, _, opts := parseOptionsBlock(sc2405Block)

	require.Len(t, opts, 1, "the daemon stamp must not be offered as an answer")
	assert.Equal(t, "1", opts[0].ID)
	for _, o := range opts {
		assert.NotEqual(t, "daemon", o.ID, "the provenance stamp is not a choice")
	}
}

// A block with one real answer is a dead end: the card must say so rather than
// present it as a decision a human can make.
func TestAttachOpenOptionsRedsSingleAnswerBlock(t *testing.T) {
	card := &BoardCard{}
	attachOpenOptions(card, []tracker.Comment{cmt(sc2405Block, time.Unix(1, 0))})

	assert.Equal(t, BoardFailed, card.State)
	assert.Contains(t, card.Error, "at least")
	assert.Empty(t, card.Options, "a malformed block offers no choices to the board")
}

// Reding the card must not also relaunch the stage that posted the block: that
// pair is the loop SC-751 removed, and it would re-post the same bad block.
func TestSingleAnswerBlockIsACleanStop(t *testing.T) {
	comments := []tracker.Comment{cmt(sc2405Block, time.Unix(1, 0))}

	assert.True(t, stagePausedOnOptions(comments, BoardImplementation),
		"a malformed decision ends the stage cleanly; the card carries the error instead")
}

// A well-formed decision is untouched by the floor.
func TestAttachOpenOptionsKeepsTwoAnswerBlock(t *testing.T) {
	body := "[human:options]\nstage: implementation\ncontext: two defensible directions\n" +
		"1: Narrow the fix to the reported path\n2: Fix the class across all callers\ndaemon: 4f3add9a"

	card := &BoardCard{}
	attachOpenOptions(card, []tracker.Comment{cmt(body, time.Unix(1, 0))})

	assert.NotEqual(t, BoardFailed, card.State)
	require.Len(t, card.Options, 2, "the daemon stamp is dropped, both real answers survive")
	assert.Equal(t, "two defensible directions", card.OptionsContext)
}
