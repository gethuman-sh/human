package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyMarker(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantStage BoardStage
		wantState BoardState
		wantOK    bool
	}{
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
		{"quoted header mid-body is not a marker", "discussion: [human:planning-started]", "", "", false},
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
