package board

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// fakePruneStore records what it was asked to keep.
type fakePruneStore struct {
	calls []map[string]struct{}
}

func (f *fakePruneStore) PruneExcept(_ string, keys map[string]struct{}) error {
	f.calls = append(f.calls, keys)
	return nil
}

func pmResults(issues ...tracker.Issue) []daemon.TrackerIssuesResult {
	return []daemon.TrackerIssuesResult{{
		TrackerName: "human", TrackerKind: "shortcut", TrackerRole: "pm", Issues: issues,
	}}
}

// The defect: the guard judged one fetch while the keep set came from another,
// so a healthy listing could authorise a prune against a board that had
// answered with nothing — taking every hidden flag and every hand-sorted column
// with it. Deriving the keep set from the guarded results is what makes the
// guard mean something.
func TestPrefsKeep_comesFromTheFetchTheGuardJudges(t *testing.T) {
	results := pmResults(tracker.Issue{Key: "SC-1"}, tracker.Issue{Key: "SC-2"})

	assert.Equal(t, map[string]struct{}{"SC-1": {}, "SC-2": {}}, PrefsKeep(results))
	assert.True(t, CanPrune(results), "the same results must satisfy the guard")
}

// A ticket that left the board but is still in the listing keeps its
// preference: dropping it early is the expensive mistake.
func TestPrefsKeep_keepsATicketThatLeftTheBoard(t *testing.T) {
	done := tracker.Issue{Key: "SC-9", StatusType: tracker.CategoryDone}

	assert.Contains(t, PrefsKeep(pmResults(done)), "SC-9")
}

func TestIdeaKeep_onlyIdeas(t *testing.T) {
	idea := tracker.Issue{Key: "SC-3", Labels: []string{"human/idea"}}
	plain := tracker.Issue{Key: "SC-4"}

	keep := IdeaKeep(pmResults(idea, plain))

	assert.Contains(t, keep, "SC-3")
	assert.NotContains(t, keep, "SC-4")
}

// No PM tracker means nothing is known, and nothing known must never read as
// "no ticket exists".
func TestPrefsKeep_noPMResultKeepsNothingAndPrunesNothing(t *testing.T) {
	results := []daemon.TrackerIssuesResult{{TrackerName: "x", TrackerKind: "github"}}
	store := &fakePruneStore{}

	PrunePrefs(results, "proj", PruneTarget{Store: store, Keep: PrefsKeep(results)})

	assert.Empty(t, store.calls, "an unknown board must not prune")
}

// The safety net: even past the guard, an empty keep set is refused. Wiping
// every preference is not a tidy-up, and it is what the reported failure looked
// like from the outside.
func TestPrunePrefs_neverPrunesEverythingAtOnce(t *testing.T) {
	results := pmResults(tracker.Issue{Key: "SC-1"})
	store := &fakePruneStore{}

	PrunePrefs(results, "proj", PruneTarget{Store: store, Keep: map[string]struct{}{}})

	assert.Empty(t, store.calls, "an empty keep set must not reach the store")
}

// A healthy fetch still prunes what genuinely went away.
func TestPrunePrefs_stillPrunesStaleEntries(t *testing.T) {
	results := pmResults(tracker.Issue{Key: "SC-1"})
	store := &fakePruneStore{}

	PrunePrefs(results, "proj", PruneTarget{Store: store, Keep: PrefsKeep(results)})

	assert.Equal(t, []map[string]struct{}{{"SC-1": {}}}, store.calls)
}
