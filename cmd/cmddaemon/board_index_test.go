package cmddaemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/recall"
)

// searchIndex opens the index written by indexBoardView and queries it.
func searchIndex(t *testing.T, dbPath, query string) []recall.Entry {
	t.Helper()
	store, err := recall.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	found, err := store.Search(context.Background(), query, 20)
	require.NoError(t, err)
	return found
}

// viewWith builds a composed board carrying one ticket.
func viewWith(card daemon.BoardViewCard) daemon.BoardView {
	return daemon.BoardView{Cards: []daemon.BoardViewCard{card}}
}

// The index is what agents consult to find out whether a problem is already
// being worked on. It was fed only by a hand-run command and sat empty for
// months while the board beside it stayed minutes-fresh — so a search that
// should have found a sibling ticket confidently returned nothing (SC-2132).
func TestIndexBoardView_MakesTheBoardSearchable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")

	indexBoardView(context.Background(), viewWith(daemon.BoardViewCard{
		Key:         "SC-1",
		Title:       "Deploy blames CI when it cannot read its credentials",
		Description: "the vault session expired and the failure was misattributed",
		Tracker:     "human",
		TrackerKind: "shortcut",
		URL:         "https://example/1",
		Assignee:    "stephan",
		Stage:       string(daemon.BoardBacklog),
	}), db, zerolog.Nop())

	found := searchIndex(t, db, "credentials")
	require.Len(t, found, 1, "a composed card must be findable by its title")
	assert.Equal(t, "SC-1", found[0].Key)
	assert.Equal(t, "human", found[0].Source)
	assert.Equal(t, "shortcut", found[0].Kind)
	assert.Equal(t, "https://example/1", found[0].URL)
}

// The description is the FTS payload — the half of a ticket that carries the
// words a sibling search actually matches on.
func TestIndexBoardView_IndexesTheDescriptionNotJustTheTitle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")

	indexBoardView(context.Background(), viewWith(daemon.BoardViewCard{
		Key:         "SC-2",
		Title:       "unrelated wording",
		Description: "the vault session expired and the failure was misattributed",
		Tracker:     "human",
	}), db, zerolog.Nop())

	found := searchIndex(t, db, "misattributed")
	require.Len(t, found, 1, "the description must be searchable, not only the title")
	assert.Equal(t, "SC-2", found[0].Key)
}

// Re-indexing the same ticket must update it rather than accumulate duplicates:
// this runs on every board fetch.
func TestIndexBoardView_ReindexingUpdatesInPlace(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	card := daemon.BoardViewCard{Key: "SC-3", Title: "first title", Tracker: "human"}

	indexBoardView(context.Background(), viewWith(card), db, zerolog.Nop())
	card.Title = "second title"
	indexBoardView(context.Background(), viewWith(card), db, zerolog.Nop())

	assert.Empty(t, searchIndex(t, db, "first"), "the stale title must not linger")
	assert.Len(t, searchIndex(t, db, "second"), 1, "the ticket is updated, not duplicated")
}

// An index write must never fail a board fetch — the board is the user's, the
// index is a convenience.
func TestIndexBoardView_UnwritableIndexIsNotFatal(t *testing.T) {
	unwritable := filepath.Join(t.TempDir(), "no-such-dir", "index.db")

	assert.NotPanics(t, func() {
		indexBoardView(context.Background(), viewWith(daemon.BoardViewCard{Key: "SC-4", Title: "one"}), unwritable, zerolog.Nop())
	})
}

// An empty board writes nothing rather than opening the store to do no work.
func TestIndexBoardView_EmptyViewIsANoop(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")

	indexBoardView(context.Background(), daemon.BoardView{}, db, zerolog.Nop())

	assert.NoFileExists(t, db, "an empty board must not even create the index file")
}
