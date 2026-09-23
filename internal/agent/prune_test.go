package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only records that ended before the retention go; a running record stays
// whatever its age, and a record that never wrote a stop time is judged by
// its start (SC-5248).
func TestPruneStoppedMetas(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	for _, m := range []Meta{
		{Name: "running-old", Status: StatusRunning, CreatedAt: old},
		{Name: "stopped-old", Status: StatusStopped, CreatedAt: old, StoppedAt: old.Add(time.Hour)},
		{Name: "failed-no-stop", Status: StatusFailed, CreatedAt: old},
		{Name: "stopped-recent", Status: StatusStopped, CreatedAt: now.Add(-2 * time.Hour), StoppedAt: now.Add(-time.Hour)},
	} {
		require.NoError(t, WriteMeta(m))
	}

	removed, err := PruneStoppedMetas(now, StoppedMetaRetention)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"stopped-old", "failed-no-stop"}, removed)

	left, err := ListMetas()
	require.NoError(t, err)
	names := make([]string, 0, len(left))
	for _, m := range left {
		names = append(names, m.Name)
	}
	assert.ElementsMatch(t, []string{"running-old", "stopped-recent"}, names)
}
