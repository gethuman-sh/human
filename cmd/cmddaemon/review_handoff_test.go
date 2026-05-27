package cmddaemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestLatestReadyKeys(t *testing.T) {
	t0 := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		comments []tracker.Comment
		want     []string
	}{
		{
			name: "single handoff returns its keys",
			comments: []tracker.Comment{
				{Body: "plain comment", Created: t0},
				{Body: "[human:ready-for-review]\nengineering: HUM-89", Created: t0.Add(time.Minute)},
			},
			want: []string{"HUM-89"},
		},
		{
			name: "later review-complete clears the flag",
			comments: []tracker.Comment{
				{Body: "[human:ready-for-review]\nengineering: HUM-89", Created: t0},
				{Body: "[human:review-complete]\nverdict: pass", Created: t0.Add(time.Minute)},
			},
			want: nil,
		},
		{
			name: "newer handoff after a review-complete re-flags",
			comments: []tracker.Comment{
				{Body: "[human:ready-for-review]\nengineering: HUM-89", Created: t0},
				{Body: "[human:review-complete]\nverdict: fail", Created: t0.Add(time.Minute)},
				{Body: "[human:ready-for-review]\nengineering: HUM-89, HUM-90", Created: t0.Add(2 * time.Minute)},
			},
			want: []string{"HUM-89", "HUM-90"},
		},
		{
			name: "no handoff means no keys",
			comments: []tracker.Comment{
				{Body: "plain comment", Created: t0},
			},
			want: nil,
		},
		{
			name: "comments not sorted by time still resolved correctly",
			comments: []tracker.Comment{
				{Body: "[human:review-complete]\nverdict: pass", Created: t0.Add(time.Minute)},
				{Body: "[human:ready-for-review]\nengineering: HUM-89", Created: t0},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := latestReadyKeys(tt.comments)
			assert.Equal(t, tt.want, got)
		})
	}
}
