package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestClassifyMarker(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantStage BoardStage
		wantState BoardState
		wantOK    bool
	}{
		// The gate runs before the pipeline, so both markers sit in backlog: a
		// card must show the review running rather than looking idle, and must
		// stop showing it once the verdict lands.
		{"ticket-review-started", "[human:ticket-review-started]", BoardBacklog, BoardRunning, true},
		{"ticket-review verdict", "[human:ticket-review] ready\nroot: same as ticket", BoardBacklog, BoardDone, true},
		{"planning-started", "[human:planning-started]", BoardPlanning, BoardRunning, true},
		{"plan-ready", "[human:plan-ready]\nengineering: HUM-7", BoardPlanning, BoardDone, true},
		{"planning-failed", "[human:planning-failed]\nboom", BoardPlanning, BoardFailed, true},
		{"ready-for-review is implementation done", "[human:ready-for-review]\nengineering: HUM-7", BoardImplementation, BoardDone, true},
		{"implementation-started", "[human:implementation-started]", BoardImplementation, BoardRunning, true},
		{"implementation-failed", "[human:implementation-failed]\nx", BoardImplementation, BoardFailed, true},
		{"review-started", "[human:review-started]", BoardVerification, BoardRunning, true},
		{"review-complete is verification done", "[human:review-complete]", BoardVerification, BoardDone, true},
		{"review-failed", "[human:review-failed]\nx", BoardVerification, BoardFailed, true},
		{"pr-started", "[human:pr-started]", BoardDoneStage, BoardRunning, true},
		{"pr-pushed", "[human:pr-pushed]\npr: https://x", BoardDoneStage, BoardDone, true},
		{"pr-failed", "[human:pr-failed]\nx", BoardDoneStage, BoardFailed, true},
		// Outage markers are the non-failing transient twin per stage (SC-2307).
		{"planning-outage", "[human:planning-outage]\nop timed out", BoardPlanning, BoardOutage, true},
		{"implementation-outage", "[human:implementation-outage]\nop timed out", BoardImplementation, BoardOutage, true},
		{"review-outage", "[human:review-outage]\ntracker unreachable", BoardVerification, BoardOutage, true},
		{"deploy-outage", "[human:deploy-outage]\ntracker unreachable", BoardDoneStage, BoardOutage, true},
		{"quoted header mid-body is not a marker", "discussion: [human:planning-started]", "", "", false},
		// The related-work triage markers are advisory and deliberately kept out of
		// orderedMarkerSpecs, so they must never classify as a board stage (SC-2405).
		{"related found is not a board marker", "[human:related] found\n- duplicate of SC-9", "", "", false},
		{"related-started is not a board marker", "[human:related-started]", "", "", false},
		// The shipped-partial trace decorates the card, it never moves it — like
		// the related markers it is kept out of orderedMarkerSpecs (SC-2910).
		{"shipped-partial is not a board marker", "[human:shipped-partial]\nfollow-on: SC-1\ndeferred: A", "", "", false},
		{"non-marker", "just a normal comment", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stage, state, ok := ClassifyMarker(tt.body)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantStage, stage)
			assert.Equal(t, tt.wantState, state)
		})
	}
}

func TestHasCompletedRelatedRecord(t *testing.T) {
	now := time.Unix(1000, 0)
	tests := []struct {
		name string
		body string
		want bool
	}{
		// found and none are the two completed terminals: the search ran to a
		// verdict, so the menu action is suppressed.
		{"found is complete", "[human:related] found\n- duplicate of SC-9", true},
		{"none is complete", "[human:related] none", true},
		// incomplete is a visible record that the run died halfway — it must NOT
		// suppress the re-run, so it does not count as a completed record.
		{"incomplete is not complete", "[human:related] incomplete\ncould not search", false},
		// The started marker shares a prefix up to the closing bracket; it must
		// never be mistaken for a completed record.
		{"related-started is not a record", "[human:related-started]", false},
		{"no related marker", "just a comment", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCompletedRelatedRecord([]tracker.Comment{{Body: tt.body, Created: now}})
			assert.Equal(t, tt.want, got)
		})
	}
}
