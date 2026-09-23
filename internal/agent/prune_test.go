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

// A stopped record can be relaunched under the same name (board agent names
// are deterministic and reused) between the snapshot PruneStoppedMetas took
// and the delete. deleteStoppedMetaLocked must re-read the record under its
// lock and skip a delete when the record it now sees is no longer the
// stopped one the snapshot decided to retire (SC-5248).
func TestDeleteStoppedMetaLocked_SkipsAgentRelaunchedSinceSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	candidate := Meta{Name: "board-x-planning", Status: StatusStopped, CreatedAt: old, StoppedAt: old.Add(time.Hour)}
	require.NoError(t, WriteMeta(candidate))

	// Relaunch: the record is rewritten as running after the snapshot that
	// produced `candidate` was taken, but before the delete runs.
	relaunched := Meta{Name: "board-x-planning", Status: StatusRunning, CreatedAt: time.Now()}
	require.NoError(t, WriteMeta(relaunched))

	deleted, err := deleteStoppedMetaLocked(candidate)
	require.NoError(t, err)
	assert.False(t, deleted, "must not delete a record that was relaunched since the snapshot")

	current, err := ReadMeta("board-x-planning")
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, current.Status, "the relaunch's record must survive")
}

// The ordinary case: nothing raced the snapshot, so the stopped record is
// still the one on disk and the delete proceeds.
func TestDeleteStoppedMetaLocked_DeletesUnchangedRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	candidate := Meta{Name: "board-y-planning", Status: StatusStopped, CreatedAt: old, StoppedAt: old.Add(time.Hour)}
	require.NoError(t, WriteMeta(candidate))

	deleted, err := deleteStoppedMetaLocked(candidate)
	require.NoError(t, err)
	assert.True(t, deleted)

	_, err = ReadMeta("board-y-planning")
	assert.Error(t, err, "the record must be gone")
}
