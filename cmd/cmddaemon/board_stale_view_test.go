package cmddaemon

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/boardcache"
	"github.com/gethuman-sh/human/internal/daemon"
)

func tmpCache(t *testing.T) *boardcache.Store {
	t.Helper()
	return boardcache.NewStore(filepath.Join(t.TempDir(), "boardcache.json"))
}

func workingView() daemon.BoardView {
	return daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", Title: "one"}}}
}

// A refresh that cannot complete must not present as a clean, empty workspace:
// an empty board is indistinguishable from having no work at all (SC-2132).
func TestServeLastGoodView_FailedRefreshKeepsTheLastBoard(t *testing.T) {
	cache := tmpCache(t)
	rememberBoardView(cache, "/proj", workingView(), zerolog.Nop())

	view, err := serveLastGoodView(cache, "/proj", errors.New("tracker unreachable"), zerolog.Nop())

	require.NoError(t, err)
	require.Len(t, view.Cards, 1)
	assert.Equal(t, "SC-1", view.Cards[0].Key)
}

// Keeping the cards must not hide the failure — a silently stale board that
// looks healthy trades a visible problem for an invisible one.
func TestServeLastGoodView_SaysItIsStale(t *testing.T) {
	cache := tmpCache(t)
	rememberBoardView(cache, "/proj", workingView(), zerolog.Nop())

	view, err := serveLastGoodView(cache, "/proj", errors.New("boom"), zerolog.Nop())

	require.NoError(t, err)
	assert.Contains(t, view.Error, "this refresh failed")
	assert.Contains(t, view.Error, "boom", "the cause travels with the notice")
}

// With nothing remembered there is nothing truer to show than the failure.
func TestServeLastGoodView_NothingRememberedSurfacesTheError(t *testing.T) {
	cause := errors.New("boom")

	view, err := serveLastGoodView(tmpCache(t), "/proj", cause, zerolog.Nop())

	require.ErrorIs(t, err, cause)
	assert.Empty(t, view.Cards)
}

// An empty board is never remembered: one bad refresh would otherwise become
// the "last good" one and pin the board empty for good.
func TestRememberBoardView_EmptyBoardIsNotRemembered(t *testing.T) {
	cache := tmpCache(t)
	rememberBoardView(cache, "/proj", workingView(), zerolog.Nop())
	rememberBoardView(cache, "/proj", daemon.BoardView{}, zerolog.Nop())

	view, err := serveLastGoodView(cache, "/proj", errors.New("boom"), zerolog.Nop())

	require.NoError(t, err)
	assert.Len(t, view.Cards, 1, "the last board with work survives an empty one")
}

// Snapshots are per project: a global key evicted another project's board on
// every save (SC-1654/1692).
func TestRememberBoardView_IsKeyedPerProject(t *testing.T) {
	cache := tmpCache(t)
	rememberBoardView(cache, "/proj-a", workingView(), zerolog.Nop())
	rememberBoardView(cache, "/proj-b", daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-9"}}}, zerolog.Nop())

	a, err := serveLastGoodView(cache, "/proj-a", errors.New("boom"), zerolog.Nop())
	require.NoError(t, err)
	require.Len(t, a.Cards, 1)
	assert.Equal(t, "SC-1", a.Cards[0].Key, "one project's board must not evict another's")
}

// The remembered snapshot must round-trip as real JSON, not merely be written.
func TestRememberBoardView_RoundTripsThroughTheCache(t *testing.T) {
	cache := tmpCache(t)
	rememberBoardView(cache, "/proj", workingView(), zerolog.Nop())

	raw, ok := cache.Load("/proj")
	require.True(t, ok)
	var view daemon.BoardView
	require.NoError(t, json.Unmarshal(raw, &view))
	assert.Equal(t, "one", view.Cards[0].Title)
}
