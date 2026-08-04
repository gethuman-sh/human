package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The current-user route carries the authenticated PM-tracker user's display
// name so the board can dim cards owned by someone else (SC-3339).
func TestServer_HandleCurrentUser_ReturnsName(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.CurrentUserFetcher = func() (string, error) { return "Stephan Schmidt", nil }
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"current-user"}})
	require.Equal(t, 0, resp.ExitCode)

	var res CurrentUserResult
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &res))
	assert.Equal(t, "Stephan Schmidt", res.Name)
}

// A nil fetcher is a valid "identity unknown" answer, not an error: the board
// then dims nothing rather than failing to render.
func TestServer_HandleCurrentUser_NilFetcherIsEmptyName(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) { s.CurrentUserFetcher = nil })

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"current-user"}})
	require.Equal(t, 0, resp.ExitCode)

	var res CurrentUserResult
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &res))
	assert.Equal(t, "", res.Name)
}

// A fetcher error surfaces as an error response rather than a silent empty name.
func TestServer_HandleCurrentUser_FetcherErrorIsReported(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.CurrentUserFetcher = func() (string, error) { return "", assert.AnError }
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"current-user"}})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr+resp.Stdout, assert.AnError.Error())
}
