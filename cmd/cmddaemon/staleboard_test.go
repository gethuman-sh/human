package cmddaemon

import (
	"testing"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/stretchr/testify/assert"
)

// TestStaleBoardBanner covers SC-4151: a served snapshot said only that it was
// "the last board that loaded", so a minute-old board and a week-old one read
// identically while both went on showing spinners.
func TestStaleBoardBanner(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cause := errors.WithDetails("tracker unreachable")

	for _, tc := range []struct {
		name     string
		cachedAt string
		want     string
	}{
		{"seconds", now.Add(-30 * time.Second).Format(time.RFC3339), "(30s old)"},
		{"minutes", now.Add(-45 * time.Minute).Format(time.RFC3339), "(45m old)"},
		{"hours", now.Add(-5 * time.Hour).Format(time.RFC3339), "(5h old)"},
		{"days", now.Add(-72 * time.Hour).Format(time.RFC3339), "(3d old)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := staleBoardBanner(tc.cachedAt, now, cause)
			assert.Contains(t, got, tc.want)
			assert.Contains(t, got, "showing the last board that loaded")
			assert.Contains(t, got, "tracker unreachable")
		})
	}
}

// A snapshot written before the stamp existed must read exactly as it did.
func TestStaleBoardBanner_UnstampedSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cause := errors.WithDetails("tracker unreachable")

	for _, cachedAt := range []string{"", "not-a-time"} {
		got := staleBoardBanner(cachedAt, now, cause)
		assert.Equal(t, "showing the last board that loaded — this refresh failed: tracker unreachable", got)
	}
}
