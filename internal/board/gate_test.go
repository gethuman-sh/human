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
		{"PM truncated", []daemon.TrackerIssuesResult{{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}}, Truncated: true}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, board.CanPrune(tc.results))
		})
	}
}

func TestPrunePrefs_TruncatedFetch_LeavesStoresUntouched(t *testing.T) {
	// A capped fetch omits the tickets past the cap; pruning against it would
	// erase their saved order and hidden flags purely because the backlog grew
	// (SC-1693). The keep set holds only the one fetched key, so an unguarded
	// prune would drop SC-2/SC-3 — the guard must prevent it.
	prefs, ideas := seededStores(t)
	results := []daemon.TrackerIssuesResult{
		{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}}, Truncated: true},
	}
	keep := map[string]struct{}{"SC-1": {}}
	board.PrunePrefs(results, testProject,
		board.PruneTarget{Store: prefs, Keep: keep},
		board.PruneTarget{Store: ideas, Keep: keep},
	)
	assert.False(t, board.CanPrune(results))
	assert.Equal(t, []string{"SC-1", "SC-2", "SC-3"}, prefs.Snapshot(testProject).Columns["product"])
	_, hidden := prefs.Snapshot(testProject).Hidden["SC-2"]
	assert.True(t, hidden, "hidden flag must survive a truncated fetch")
	assert.Equal(t, map[string]int{"SC-9": 3}, ideas.Assignments(testProject))
}

func TestTruncationNotice(t *testing.T) {
	tests := []struct {
		name    string
		results []daemon.TrackerIssuesResult
		want    string
	}{
		{"no pm result", []daemon.TrackerIssuesResult{{TrackerRole: "engineering"}}, ""},
		{"complete fetch", []daemon.TrackerIssuesResult{{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}}}}, ""},
		{
			name: "truncated fetch names the shown count",
			results: []daemon.TrackerIssuesResult{
				{TrackerRole: "pm", Issues: []tracker.Issue{{Key: "SC-1"}, {Key: "SC-2"}}, Truncated: true},
			},
			want: "Showing the first 2 tickets — more open tickets exist beyond the fetch cap and are not displayed. Saved order and hidden flags are preserved (pruning is paused) until the full list can be fetched.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, board.TruncationNotice(tc.results))
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
			want:    "No tracker configured. Add one to .humanconfig so its issues appear on the board (see CLAUDE.md, \"Board rendering\").",
		},
		{
			// A LONE undeclared tracker no longer reaches this notice at all: it
			// resolves to pm whatever backend it is, so the daemon stamps the role
			// and the board renders. This is the several-candidates case, which is
			// the only one the machine genuinely cannot decide.
			name: "undeclared candidates are named so the user knows what to annotate",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "work", TrackerKind: "linear", Issues: []tracker.Issue{{Key: "ENG-1"}}},
				{TrackerName: "board", TrackerKind: "shortcut", Issues: []tracker.Issue{{Key: "SC-1"}}},
			},
			want: "More than one tracker is configured and none says which board this is. Found work (linear), board (shortcut) — add role: pm to the one whose issues belong here (see CLAUDE.md, \"Board rendering\").",
		},
		{
			name: "every result failed — the banner speaks, not role advice",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "broken", TrackerKind: "jira", Err: "boom"},
			},
			want: "",
		},
		{
			name: "a resolved tracker beside a failed one is still named",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "broken", TrackerKind: "jira", Err: "boom"},
				{TrackerName: "work", TrackerKind: "linear", Issues: []tracker.Issue{{Key: "ENG-1"}}},
			},
			want: "More than one tracker is configured and none says which board this is. Found work (linear) — add role: pm to the one whose issues belong here (see CLAUDE.md, \"Board rendering\").",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, board.PMRoleNotice(tc.results))
		})
	}
}

// The board's error channel must be reachable by a failure that belongs to no
// tracker — that is the class of failure (credentials) it was silently
// dropping (SC-3554).
func TestErrorBanner(t *testing.T) {
	tests := []struct {
		name    string
		results []daemon.TrackerIssuesResult
		want    string
	}{
		{name: "nothing failed", results: []daemon.TrackerIssuesResult{{TrackerRole: "pm", TrackerName: "prod"}}, want: ""},
		{
			name:    "a failure with no tracker identity stands on its own words",
			results: []daemon.TrackerIssuesResult{{Project: "credentials", Err: "cannot connect to 1Password app"}},
			want:    "cannot connect to 1Password app",
		},
		{
			name:    "a tracker's failure is attributed to it",
			results: []daemon.TrackerIssuesResult{{TrackerName: "work", TrackerKind: "linear", Err: "401"}},
			want:    "work (linear): 401",
		},
		{
			name:    "a named tracker with no kind is still attributed",
			results: []daemon.TrackerIssuesResult{{TrackerName: "work", Err: "401"}},
			want:    "work: 401",
		},
		{
			name: "the load failure leads — it names the cause the user can act on",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "work", TrackerKind: "linear", Err: "401"},
				{Project: "credentials", Err: "cannot connect to 1Password app"},
			},
			want: "cannot connect to 1Password app; work (linear): 401",
		},
		{
			name: "one instance failing across several projects says it once",
			results: []daemon.TrackerIssuesResult{
				{TrackerName: "work", TrackerKind: "linear", Project: "a", Err: "401"},
				{TrackerName: "work", TrackerKind: "linear", Project: "b", Err: "401"},
			},
			want: "work (linear): 401",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, board.ErrorBanner(tc.results))
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
