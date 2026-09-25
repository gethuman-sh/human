package cmddaemon

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

// placedView is a remembered board whose card is somewhere other than Backlog —
// the case the quick paint gets wrong on its own.
func placedView() daemon.BoardView {
	return daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", Title: "one", Stage: string(daemon.BoardVerification)},
	}}
}

// The cached route serves the remembered board so an opening board can place its
// cards before any stage has been derived (SC-4324).
func TestCachedBoardViewFunc_ServesTheRememberedBoard(t *testing.T) {
	dir := t.TempDir()
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	cache := tmpCache(t)
	rememberBoardView(cache, dir, placedView(), zerolog.Nop())

	view := cachedBoardViewFunc(reg, cache)()

	require.Len(t, view.Cards, 1)
	assert.Equal(t, string(daemon.BoardVerification), view.Cards[0].Stage)
}

// Nothing remembered yields an empty board rather than an error: the caller has
// already composed one and is only asking whether it can be placed better.
func TestCachedBoardViewFunc_NothingRememberedIsEmpty(t *testing.T) {
	reg, err := daemon.NewProjectRegistry([]string{t.TempDir()})
	require.NoError(t, err)

	view := cachedBoardViewFunc(reg, tmpCache(t))()

	assert.Empty(t, view.Cards)
}

// The snapshot is keyed by project, so another project's board is not this
// project's answer — the key that scoped the save must scope the read (SC-1654).
func TestCachedBoardViewFunc_AnotherProjectsSnapshotIsNotServed(t *testing.T) {
	reg, err := daemon.NewProjectRegistry([]string{t.TempDir()})
	require.NoError(t, err)
	cache := tmpCache(t)
	rememberBoardView(cache, "/some/other/project", placedView(), zerolog.Nop())

	view := cachedBoardViewFunc(reg, cache)()

	assert.Empty(t, view.Cards)
}

// The cached board is served plain. serveLastGoodView's staleness banner says
// "this refresh failed"; here a refresh is running behind the paint, so the same
// sentence would name a failure that has not happened.
func TestCachedBoardViewFunc_CarriesNoStaleBanner(t *testing.T) {
	dir := t.TempDir()
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	cache := tmpCache(t)
	rememberBoardView(cache, dir, placedView(), zerolog.Nop())

	view := cachedBoardViewFunc(reg, cache)()

	assert.Empty(t, view.Error)
}

// The remembered snapshot can be arbitrarily old, so a flow claim decoded from
// it is a statement about a moment that has passed — exactly the claim
// serveLastGoodView drops for the same reason (SC-3577). Both routes share one
// decode point, so this must hold here even though this route carries no
// staleness banner of its own.
func TestCachedBoardViewFunc_DropsTheFlowClaim(t *testing.T) {
	dir := t.TempDir()
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	cache := tmpCache(t)
	view := placedView()
	view.Flow = &daemon.BoardFlow{State: daemon.BoardFlowFlowing}
	rememberBoardView(cache, dir, view, zerolog.Nop())

	served := cachedBoardViewFunc(reg, cache)()

	assert.Nil(t, served.Flow, "a cached board this old cannot claim work is flowing")
}
