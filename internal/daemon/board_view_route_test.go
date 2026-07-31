package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The board-view route exists so every consumer reads one composed picture
// instead of assembling its own from raw results (SC-2132).
func TestServer_HandleBoardView_ReturnsTheComposedBoard(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.BoardViewFetcher = func() (BoardView, error) {
			return BoardView{
				DockerAvailable: true,
				Cards: []BoardViewCard{
					{Key: "SC-1", Title: "one", Stage: string(BoardBacklog)},
				},
			}, nil
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-view"}})
	require.Equal(t, 0, resp.ExitCode)

	var view BoardView
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &view))
	require.Len(t, view.Cards, 1)
	assert.Equal(t, "SC-1", view.Cards[0].Key)
	assert.True(t, view.DockerAvailable,
		"docker availability is the daemon host's verdict, carried on the view")
}

// A daemon with no composer must say so rather than answer with an empty board:
// "no cards" and "cannot compose" are different states, and an empty board reads
// as "there is no work".
func TestServer_HandleBoardView_NoComposerIsAnError(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) { s.BoardViewFetcher = nil })

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-view"}})

	assert.NotEqual(t, 0, resp.ExitCode, "an unavailable board must not read as an empty one")
}

// A compose failure is reported, not swallowed into an empty board.
func TestServer_HandleBoardView_ComposeFailureIsReported(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.BoardViewFetcher = func() (BoardView, error) {
			return BoardView{}, assert.AnError
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-view"}})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr+resp.Stdout, assert.AnError.Error())
}
