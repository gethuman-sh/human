package recall

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// planned builds a provider whose tickets each carry a [human:plan] comment.
type planned struct {
	*mockProvider
	plans map[string]string
}

func (p planned) ListComments(_ context.Context, key string) ([]tracker.Comment, error) {
	body, ok := p.plans[key]
	if !ok {
		return nil, nil
	}
	return []tracker.Comment{{
		ID: "1", Body: "[human:plan]\n" + body, Created: time.Unix(1, 0),
	}}, nil
}

func TestExtractFilePaths(t *testing.T) {
	got := ExtractFilePaths(`Change internal/daemon/board_transition.go and
	desktop/frontend/src/board.ts. See e.g. version v1.2 and a bare main.go.`)

	assert.Equal(t, []string{"desktop/frontend/src/board.ts", "internal/daemon/board_transition.go"}, got)
	assert.NotContains(t, got, "main.go", "a bare filename is too weak a signal to tie tickets together")
}

func TestExtractFilePaths_DeduplicatesAndIgnoresProse(t *testing.T) {
	got := ExtractFilePaths("a/b.go and again a/b.go; nothing here, e.g. no paths at all.")
	assert.Equal(t, []string{"a/b.go"}, got)
	assert.Empty(t, ExtractFilePaths("plain prose with no paths"))
}

// THE OUTCOME TEST. SC-1996 ("Deploy blames CI when it cannot read its own
// credentials") and SC-2042 ("The reason a secret read failed never reaches the
// code that reports it") describe ONE defect and were implemented independently,
// colliding in the same function. They share no title or description words —
// only the file their plans name.
//
// If this test fails, the whole exercise has failed on its own terms, however
// many rows the index holds.
func TestOverlap_TicketsSharingOnlyAFileAreConnected(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	issues := []tracker.Issue{
		{Key: "SC-1996", Title: "Deploy blames CI when it cannot read its own credentials",
			Description: "the vault session expired and the deploy reported a check failure"},
		{Key: "SC-2042", Title: "The reason a secret read failed never reaches the code that reports it",
			Description: "not signed in, no such secret and store unreachable arrive undifferentiated"},
	}
	prov := planned{
		mockProvider: listing(issues...),
		plans: map[string]string{
			"SC-1996": "Tag the unreadable state in internal/daemon/board_transition.go so the headline stops blaming CI.",
			"SC-2042": "Add typed vault errors and map them in internal/daemon/board_transition.go.",
		},
	}

	_, err := Sync(ctx, s, []tracker.Instance{{Name: "human", Kind: "shortcut", Provider: prov}}, false, io.Discard)
	require.NoError(t, err)

	// The premise: no shared words, so full text cannot connect them.
	byWords, err := s.Search(ctx, "Deploy blames CI when it cannot read its own credentials", 20)
	require.NoError(t, err)
	require.NotEmpty(t, byWords)
	assert.Equal(t, "SC-1996", byWords[0].Key, "each ticket is findable by its own wording")

	// The signal that does connect them: the file both plans name.
	byFile, err := s.SearchByFile(ctx, "internal/daemon/board_transition.go", 20)
	require.NoError(t, err)
	keys := []string{byFile[0].Key, byFile[1].Key}
	require.Len(t, byFile, 2, "both tickets changing this file must be found")
	assert.ElementsMatch(t, []string{"SC-1996", "SC-2042"}, keys,
		"the file is what ties two tickets describing one problem together")
}

// A file nobody's plan names returns nothing — the lookup must be exact, not a
// ranking of tickets that merely share the path's common words.
func TestOverlap_UnrelatedFileFindsNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	prov := planned{
		mockProvider: listing(tracker.Issue{Key: "SC-1", Title: "one", Description: "body"}),
		plans:        map[string]string{"SC-1": "Change internal/daemon/board_transition.go."},
	}
	_, err := Sync(ctx, s, []tracker.Instance{{Name: "human", Kind: "shortcut", Provider: prov}}, false, io.Discard)
	require.NoError(t, err)

	found, err := s.SearchByFile(ctx, "internal/daemon/board_state.go", 20)
	require.NoError(t, err)
	assert.Empty(t, found, "a neighbouring file in the same package is not the same file")
}

// A re-planned ticket touches a different set of files; the old set must not
// keep reporting an overlap that no longer exists.
func TestOverlap_ReplanningReplacesTheFileSet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst := func(plan string) []tracker.Instance {
		return []tracker.Instance{{Name: "human", Kind: "shortcut", Provider: planned{
			mockProvider: listing(tracker.Issue{Key: "SC-1", Title: "one", Description: "body"}),
			plans:        map[string]string{"SC-1": plan},
		}}}
	}

	_, err := Sync(ctx, s, inst("Change internal/a/first.go."), false, io.Discard)
	require.NoError(t, err)
	_, err = Sync(ctx, s, inst("Actually change internal/b/second.go."), false, io.Discard)
	require.NoError(t, err)

	stale, err := s.SearchByFile(ctx, "internal/a/first.go", 20)
	require.NoError(t, err)
	assert.Empty(t, stale, "the abandoned path must stop reporting an overlap")

	current, err := s.SearchByFile(ctx, "internal/b/second.go", 20)
	require.NoError(t, err)
	assert.Len(t, current, 1)
}

// A file lookup against an unusable index must refuse, exactly as a text search
// does — "nobody else is touching it" is the answer that costs a duplicate.
func TestOverlap_FileLookupRefusesOnAnEmptyIndex(t *testing.T) {
	_, err := newTestStore(t).SearchByFile(context.Background(), "internal/daemon/board_transition.go", 20)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexEmpty)
}
