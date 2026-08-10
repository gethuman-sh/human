package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The board-view-cached route exists so the quick paint can place its cards
// where the board last had them instead of stacking them all in Backlog while
// the real stages are still being derived (SC-4324).
func TestServer_HandleCachedBoardView_ReturnsTheRememberedBoard(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.CachedBoardViewFetcher = func() BoardView {
			return BoardView{Cards: []BoardViewCard{
				{Key: "SC-1", Title: "one", Stage: string(BoardVerification)},
			}}
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-view-cached"}})
	require.Equal(t, 0, resp.ExitCode)

	var view BoardView
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &view))
	require.Len(t, view.Cards, 1)
	assert.Equal(t, string(BoardVerification), view.Cards[0].Stage)
}

// Nothing remembered is an answer, not a failure: the caller already has a board
// and is only asking whether it can be improved. Erroring here would buy the
// caller a fallback it has to write anyway.
func TestServer_HandleCachedBoardView_NothingRememberedIsAnEmptyBoard(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) { s.CachedBoardViewFetcher = nil })

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-view-cached"}})
	require.Equal(t, 0, resp.ExitCode, "an unremembered board is not a broken daemon")

	var view BoardView
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &view))
	assert.Empty(t, view.Cards)
}
