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

	name, at := board.LatestActivity(entries)

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

	name, _ := board.LatestActivity(entries)

	assert.Equal(t, "fix", name, "the run's own phase is the informative one")
}

// A run that recorded nothing gets nothing shown. Inventing a phase would be the
// same failure as the spinner: an assertion nobody checked.
func TestLatestActivity_SaysNothingWhenNothingWasRecorded(t *testing.T) {
	name, at := board.LatestActivity(nil)
	assert.Empty(t, name)
	assert.True(t, at.IsZero())

	name, _ = board.LatestActivity([]agentstate.Entry{{Name: "capabilities", UpdatedAt: time.Now()}})
	assert.Empty(t, name, "only the stage namespace describes a phase")
}

func TestActivityLabel_ReadsAsWorkNotAsPipelineVocabulary(t *testing.T) {
	assert.Equal(t, "reproducing", board.ActivityLabel("triage"))
	assert.Equal(t, "verifying", board.ActivityLabel("verify"))
	assert.Empty(t, board.ActivityLabel(""))
	assert.Equal(t, "brand-new-phase", board.ActivityLabel("brand-new-phase"),
		"an unmapped phase shows the day it is added, not the day a table is remembered")
}
