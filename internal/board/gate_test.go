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

// testProject is the fixed project key threaded through every store call in
// this test file — pruning is project-scoped, but these tests exercise the
// gate's fetch-trustworthiness logic, not cross-project isolation.
const testProject = "p"

func seededStores(t *testing.T) (*boardprefs.Store, *ideaspace.Store) {
	t.Helper()
	prefs := boardprefs.NewStore(filepath.Join(t.TempDir(), "boardprefs.json"))
	require.NoError(t, prefs.SetOrder(testProject, "product", []string{"SC-1", "SC-2", "SC-3"}))
	require.NoError(t, prefs.SetHidden(testProject, "SC-2", true))
	ideas := ideaspace.NewStore(filepath.Join(t.TempDir(), "ideaspace.json"))
	require.NoError(t, ideas.Set(testProject, "SC-9", 3))
	return prefs, ideas
}

func TestPrunePrefs_NoPMResult_LeavesStoresUntouched(t *testing.T) {
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{
		{TrackerName: "eng", TrackerKind: "linear", TrackerRole: "engineering"},
	}
	board.PrunePrefs(results, testProject,
		board.PruneTarget{Store: prefs, Keep: map[string]struct{}{}},
		board.PruneTarget{Store: ideas, Keep: map[string]struct{}{}},
	)
	assert.False(t, board.CanPrune(results))
	got := prefs.Snapshot(testProject)
	assert.Equal(t, []string{"SC-1", "SC-2", "SC-3"}, got.Columns["product"])
	_, hidden := got.Hidden["SC-2"]
	assert.True(t, hidden, "hidden flag must survive a no-PM-result fetch")
	assert.Equal(t, map[string]int{"SC-9": 3}, ideas.Assignments(testProject))
}

func TestPrunePrefs_FetchError_LeavesStoresUntouched(t *testing.T) {
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{{TrackerRole: "pm", Err: "boom"}}
	board.PrunePrefs(results, testProject,
		board.PruneTarget{Store: prefs, Keep: map[string]struct{}{}},
		board.PruneTarget{Store: ideas, Keep: map[string]struct{}{}},
	)
	assert.False(t, board.CanPrune(results))
	assert.Equal(t, []string{"SC-1", "SC-2", "SC-3"}, prefs.Snapshot(testProject).Columns["product"])
	assert.Equal(t, map[string]int{"SC-9": 3}, ideas.Assignments(testProject))
}

func TestPrunePrefs_SuccessfulFetch_PrunesVanished(t *testing.T) {
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{
		{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}}},
	}
	keep := map[string]struct{}{"SC-1": {}}
	board.PrunePrefs(results, testProject,
		board.PruneTarget{Store: prefs, Keep: keep},
		board.PruneTarget{Store: ideas, Keep: keep},
	)
	assert.True(t, board.CanPrune(results))
	assert.Equal(t, []string{"SC-1"}, prefs.Snapshot(testProject).Columns["product"])
	_, hidden := prefs.Snapshot(testProject).Hidden["SC-2"]
	assert.False(t, hidden, "hidden flag for a vanished ticket must be pruned")
	assert.Equal(t, map[string]int{}, ideas.Assignments(testProject))
}

// TestPrunePrefs_SecondProjectFetch_PreservesFirstProject is the regression
// test for SC-1692: opening a second project must never prune the first
// project's saved state, since PrunePrefs previously scoped nothing by
// project at all.
func TestPrunePrefs_SecondProjectFetch_PreservesFirstProject(t *testing.T) {
	const projA = "/home/u/cli"
	const projB = "ROO-1"

	prefs := boardprefs.NewStore(filepath.Join(t.TempDir(), "boardprefs.json"))
	require.NoError(t, prefs.SetOrder(projA, "product", []string{"42", "48", "49"}))
	require.NoError(t, prefs.SetHidden(projA, "42", true))
	ideas := ideaspace.NewStore(filepath.Join(t.TempDir(), "ideaspace.json"))
	require.NoError(t, ideas.Set(projA, "165", 3))

	results := []daemon.TrackerIssuesResult{
		{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "ROO-1"}}},
	}
	keep := map[string]struct{}{"ROO-1": {}}
	board.PrunePrefs(results, projB,
		board.PruneTarget{Store: prefs, Keep: keep},
		board.PruneTarget{Store: ideas, Keep: keep},
	)

	got := prefs.Snapshot(projA)
	assert.Equal(t, []string{"42", "48", "49"}, got.Columns["product"], "project A's column order must survive a project B fetch")
	_, hidden := got.Hidden["42"]
	assert.True(t, hidden, "project A's hidden flag must survive a project B fetch")
	assert.Equal(t, map[string]int{"165": 3}, ideas.Assignments(projA), "project A's idea placement must survive a project B fetch")
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

func TestPMRoleNotice(t *testing.T) {
	tests := []struct {
		name    string
		results []daemon.TrackerIssuesResult
		want    string
	}{
		{
			name:    "pm result present renders normally",
			results: []daemon.TrackerIssuesResult{{TrackerRole: "pm", TrackerName: "prod"}},
			want:    "",
		},
		{
			name:    "no trackers at all gives generic hint",
			results: nil,
			want:    "No PM-role tracker configured. Add role: pm to a tracker in .humanconfig so its issues appear on the board (see CLAUDE.md, \"Board rendering\").",
		},
		{
			name: "non-pm tracker is named so the user knows what to annotate",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "work", TrackerKind: "linear", Issues: []tracker.Issue{{Key: "ENG-1"}}},
			},
			want: "No PM-role tracker configured. Found work (linear), but none has role: pm — add role: pm to one in .humanconfig so its issues appear on the board (see CLAUDE.md, \"Board rendering\").",
		},
		{
			name: "errored trackers are excluded from the naming",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "broken", TrackerKind: "jira", Err: "boom"},
			},
			want: "No PM-role tracker configured. Add role: pm to a tracker in .humanconfig so its issues appear on the board (see CLAUDE.md, \"Board rendering\").",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, board.PMRoleNotice(tc.results))
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
