package board_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/board"
)

func TestLatestActivity_TakesTheNewestPhase(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	entries := []agentstate.Entry{
		{Name: "stage.triage", UpdatedAt: base},
		{Name: "stage.fix", UpdatedAt: base.Add(10 * time.Minute)},
		{Name: "stage.verify", UpdatedAt: base.Add(20 * time.Minute)},
	}

	name, at := board.LatestActivity(entries, time.Time{})

	assert.Equal(t, "verify", name)
	assert.Equal(t, base.Add(20*time.Minute), at)
}

// The board's own columns already say these, so repeating them tells the reader
// nothing they cannot see. stage.implementation in particular is the board stage
// the whole fix run reports under — showing it would restate the column.
func TestLatestActivity_IgnoresPhasesTheColumnAlreadyShows(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	entries := []agentstate.Entry{
		{Name: "stage.fix", UpdatedAt: base},
		{Name: "stage.implementation", UpdatedAt: base.Add(time.Hour)},
	}

	name, _ := board.LatestActivity(entries, time.Time{})

	assert.Equal(t, "fix", name, "the run's own phase is the informative one")
}

// A run that recorded nothing gets nothing shown. Inventing a phase would be the
// same failure as the spinner: an assertion nobody checked.
func TestLatestActivity_SaysNothingWhenNothingWasRecorded(t *testing.T) {
	name, at := board.LatestActivity(nil, time.Time{})
	assert.Empty(t, name)
	assert.True(t, at.IsZero())

	name, _ = board.LatestActivity([]agentstate.Entry{{Name: "capabilities", UpdatedAt: time.Now()}}, time.Time{})
	assert.Empty(t, name, "only the stage namespace describes a phase")
}

// The store is keyed on the ticket alone, so a phase record from an earlier,
// unrelated stage of the SAME ticket sits in the same list. Without a lower
// bound, a run that crashed before writing anything of its own would borrow
// that leftover phase and assert it as its own (SC-3656 PR review finding).
func TestLatestActivity_IgnoresEntriesBeforeTheRunStarted(t *testing.T) {
	base := time.Now().Add(-24 * time.Hour)
	entries := []agentstate.Entry{
		{Name: "stage.pr-review", UpdatedAt: base}, // a previous, unrelated run
	}

	name, at := board.LatestActivity(entries, time.Now().Add(-time.Hour))

	assert.Empty(t, name, "an entry older than the run's own start must not be borrowed")
	assert.True(t, at.IsZero())
}

// A phase this run itself wrote, at or after its own start, is still found.
func TestLatestActivity_KeepsEntriesFromTheRunItself(t *testing.T) {
	since := time.Now().Add(-time.Hour)
	entries := []agentstate.Entry{
		{Name: "stage.pr-review", UpdatedAt: since.Add(-30 * time.Minute)}, // before the run started
		{Name: "stage.fix", UpdatedAt: since.Add(10 * time.Minute)},        // this run's own write
	}

	name, at := board.LatestActivity(entries, since)

	assert.Equal(t, "fix", name)
	assert.Equal(t, since.Add(10*time.Minute), at)
}

func TestActivityLabel_ReadsAsWorkNotAsPipelineVocabulary(t *testing.T) {
	assert.Equal(t, "reproducing", board.ActivityLabel("triage"))
	assert.Equal(t, "verifying", board.ActivityLabel("verify"))
	assert.Empty(t, board.ActivityLabel(""))
	assert.Equal(t, "brand-new-phase", board.ActivityLabel("brand-new-phase"),
		"an unmapped phase shows the day it is added, not the day a table is remembered")
}
