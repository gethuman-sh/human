package board_test

import (
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/internal/board"
	"github.com/gethuman-sh/human/internal/boardprefs"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/ideaspace"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seededStores(t *testing.T) (*boardprefs.Store, *ideaspace.Store) {
	t.Helper()
	prefs := boardprefs.NewStore(filepath.Join(t.TempDir(), "boardprefs.json"))
	require.NoError(t, prefs.SetOrder("product", []string{"SC-1", "SC-2", "SC-3"}))
	require.NoError(t, prefs.SetHidden("SC-2", true))
	ideas := ideaspace.NewStore(filepath.Join(t.TempDir(), "ideaspace.json"))
	require.NoError(t, ideas.Set("SC-9", 3))
	return prefs, ideas
}

func TestPrunePrefs_NoPMResult_LeavesStoresUntouched(t *testing.T) {
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{
		{TrackerName: "eng", TrackerKind: "linear", TrackerRole: "engineering"},
	}
	board.PrunePrefs(results,
		board.PruneTarget{Store: prefs, Keep: map[string]struct{}{}},
		board.PruneTarget{Store: ideas, Keep: map[string]struct{}{}},
	)
	assert.False(t, board.CanPrune(results))
	got := prefs.Snapshot()
	assert.Equal(t, []string{"SC-1", "SC-2", "SC-3"}, got.Columns["product"])
	_, hidden := got.Hidden["SC-2"]
	assert.True(t, hidden, "hidden flag must survive a no-PM-result fetch")
	assert.Equal(t, map[string]int{"SC-9": 3}, ideas.Assignments())
}

func TestPrunePrefs_FetchError_LeavesStoresUntouched(t *testing.T) {
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{{TrackerRole: "pm", Err: "boom"}}
	board.PrunePrefs(results,
		board.PruneTarget{Store: prefs, Keep: map[string]struct{}{}},
		board.PruneTarget{Store: ideas, Keep: map[string]struct{}{}},
	)
	assert.False(t, board.CanPrune(results))
	assert.Equal(t, []string{"SC-1", "SC-2", "SC-3"}, prefs.Snapshot().Columns["product"])
	assert.Equal(t, map[string]int{"SC-9": 3}, ideas.Assignments())
}

func TestPrunePrefs_SuccessfulFetch_PrunesVanished(t *testing.T) {
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{
		{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}}},
	}
	keep := map[string]struct{}{"SC-1": {}}
	board.PrunePrefs(results,
		board.PruneTarget{Store: prefs, Keep: keep},
		board.PruneTarget{Store: ideas, Keep: keep},
	)
	assert.True(t, board.CanPrune(results))
	assert.Equal(t, []string{"SC-1"}, prefs.Snapshot().Columns["product"])
	_, hidden := prefs.Snapshot().Hidden["SC-2"]
	assert.False(t, hidden, "hidden flag for a vanished ticket must be pruned")
	assert.Equal(t, map[string]int{}, ideas.Assignments())
}

func TestCanPrune(t *testing.T) {
	tests := []struct {
		name    string
		results []daemon.TrackerIssuesResult
		want    bool
	}{
		{"no PM result", []daemon.TrackerIssuesResult{{TrackerRole: "engineering"}}, false},
		{"empty results", nil, false},
		{"PM error", []daemon.TrackerIssuesResult{{TrackerRole: "pm", Err: "x"}}, false},
		{"PM zero issues", []daemon.TrackerIssuesResult{{TrackerRole: "pm"}}, false},
		{"PM with issues", []daemon.TrackerIssuesResult{{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}}}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, board.CanPrune(tc.results))
		})
	}
}

func TestFirstPMResult(t *testing.T) {
	pm, ok := board.FirstPMResult([]daemon.TrackerIssuesResult{
		{TrackerRole: "engineering", TrackerName: "eng"},
		{TrackerRole: "pm", TrackerName: "prod"},
	})
	require.True(t, ok)
	assert.Equal(t, "prod", pm.TrackerName)
	_, ok = board.FirstPMResult([]daemon.TrackerIssuesResult{{TrackerRole: "engineering"}})
	assert.False(t, ok)
}
