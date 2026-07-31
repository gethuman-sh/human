package recall

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTwo indexes two tickets that describe ONE problem in completely different
// words — the real pair that was written twice because neither search found the
// other (SC-1996 and SC-2042).
func seedTwo(t *testing.T, s *SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.UpsertEntry(ctx,
		Entry{Key: "SC-1996", Source: "human", Kind: "shortcut", Title: "Deploy blames CI when it cannot read its own credentials"},
		"the vault session expired and the deploy reported a check failure"))
	require.NoError(t, s.UpsertEntry(ctx,
		Entry{Key: "SC-2042", Source: "human", Kind: "shortcut", Title: "The reason a secret read failed never reaches the code that reports it"},
		"not signed in, no such secret and store unreachable all arrive undifferentiated"))
}

// The defect: terms were joined with a space, which FTS5 reads as an implicit
// AND, so a question phrased as a sentence required EVERY word to appear and
// returned nothing. The caller then concluded there was no such ticket.
func TestSearch_NaturalLanguageQuestionMatchesOnAnyTerm(t *testing.T) {
	s := newTestStore(t)
	seedTwo(t, s)

	found, err := s.Search(context.Background(), "why does the deploy blame CI for a credential problem", 20)

	require.NoError(t, err)
	require.NotEmpty(t, found, "a sentence must not require every word to appear")
	assert.Equal(t, "SC-1996", found[0].Key, "the best overlap ranks first")
}

// Ranking is what makes OR usable: matching any term must not turn every search
// into "everything, unordered".
func TestSearch_RanksBestOverlapFirst(t *testing.T) {
	s := newTestStore(t)
	seedTwo(t, s)

	found, err := s.Search(context.Background(), "secret read failed undifferentiated", 20)

	require.NoError(t, err)
	require.NotEmpty(t, found)
	assert.Equal(t, "SC-2042", found[0].Key)
}

// A term nobody used still returns nothing — OR must not make every query match
// everything.
func TestSearch_UnrelatedQueryStillFindsNothing(t *testing.T) {
	s := newTestStore(t)
	seedTwo(t, s)

	found, err := s.Search(context.Background(), "kubernetes helm chart", 20)

	require.NoError(t, err)
	assert.Empty(t, found)
}

// "I could not look" must never render as "there is nothing there". An empty
// index answered every question with "no results", which is how the same work
// came to be done twice (SC-2132).
func TestSearch_EmptyIndexRefusesToAnswer(t *testing.T) {
	s := newTestStore(t)

	found, err := s.Search(context.Background(), "anything", 20)

	require.Error(t, err, "an empty index must not report a confident absence")
	assert.ErrorIs(t, err, ErrIndexEmpty)
	assert.Empty(t, found)
}

// A long-stale index is the same hazard slower: it answers about a world that
// has moved on.
func TestSearch_StaleIndexRefusesToAnswer(t *testing.T) {
	s := newTestStore(t)
	seedTwo(t, s)
	ageIndex(t, s, 48*time.Hour)
	s.StaleAfter = 24 * time.Hour

	_, err := s.Search(context.Background(), "credentials", 20)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexStale)
	assert.Contains(t, err.Error(), "human index", "the refusal must say how to fix it")
}

// A caller that genuinely wants a possibly-stale answer disables the check
// deliberately, rather than getting one by accident.
func TestSearch_StalenessCheckIsOptOut(t *testing.T) {
	s := newTestStore(t)
	seedTwo(t, s)
	ageIndex(t, s, 48*time.Hour)
	s.StaleAfter = 0

	found, err := s.Search(context.Background(), "credentials", 20)

	require.NoError(t, err)
	assert.NotEmpty(t, found)
}

// A fresh index answers normally — the guard must not fire in the common case.
func TestSearch_FreshIndexAnswers(t *testing.T) {
	s := newTestStore(t)
	seedTwo(t, s)

	found, err := s.Search(context.Background(), "credentials", 20)

	require.NoError(t, err)
	assert.NotEmpty(t, found)
}

// A blank query is a fault in the question, not in the index, and must not be
// reported as an index problem.
func TestSearch_BlankQueryIsNotAnIndexFailure(t *testing.T) {
	s := newTestStore(t)

	found, err := s.Search(context.Background(), "   ", 20)

	require.NoError(t, err, "an unusable question is the caller's, not the index's")
	assert.Empty(t, found)
	assert.False(t, errors.Is(err, ErrIndexEmpty))
}

// ageIndex backdates every entry so the staleness guard can be exercised
// without waiting.
func ageIndex(t *testing.T, s *SQLiteStore, by time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-by).UTC().Format("2006-01-02 15:04:05")
	_, err := s.db.ExecContext(context.Background(), "UPDATE entries SET indexed_at = ?", stamp)
	require.NoError(t, err)
}
