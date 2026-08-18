package daemon

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestIdeaLabelsToRemove(t *testing.T) {
	assert.Equal(t, []string{"human/idea"}, IdeaLabelsToRemove([]string{"human/idea", "urgent"}),
		"a label that meant something else survives promotion")
	assert.Equal(t, []string{tracker.IdeaLabel, "idea"}, IdeaLabelsToRemove([]string{"urgent"}),
		"no idea label in the set falls back to the canonical pair")
	assert.Equal(t, []string{tracker.IdeaLabel, "idea"}, IdeaLabelsToRemove(nil))
}

func TestValidateIdeaPromote(t *testing.T) {
	require.Error(t, ValidateIdeaPromote(IdeaPromoteRequest{}))
	require.Error(t, ValidateIdeaPromote(IdeaPromoteRequest{Key: "  "}))
	assert.NoError(t, ValidateIdeaPromote(IdeaPromoteRequest{Key: "SC-1"}))
}

func TestHandleIdeaPromote_RemovesLabelsAndPokes(t *testing.T) {
	token := "tok"
	var got IdeaPromoteRequest
	addr, hooks := startPokeServer(t, token, func(s *Server) {
		s.IdeaPromoter = func(req IdeaPromoteRequest) error {
			got = req
			return nil
		}
	})
	ch := hooks.Subscribe()
	defer hooks.Unsubscribe(ch)

	body, err := json.Marshal(IdeaPromoteRequest{Key: "SC-1", Labels: []string{"human/idea", "urgent"}})
	require.NoError(t, err)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"idea-promote", string(body)}})

	assert.Equal(t, 0, resp.ExitCode, resp.Stderr)
	assert.Equal(t, "SC-1", got.Key)
	assert.Equal(t, []string{"human/idea", "urgent"}, got.Labels,
		"the handler passes the card's full label set; the filter decides what comes off")
	// The card only leaves Ideas on the next refetch, and nothing else tells the
	// board the labels changed.
	assertPoked(t, ch)
}

func TestHandleIdeaPromote_NoPromoterConfigured(t *testing.T) {
	token := "tok"
	addr, _ := startPokeServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"idea-promote", `{"key":"SC-1"}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "idea promotion not available")
}

func TestHandleIdeaPromote_RejectsAKeylessRequest(t *testing.T) {
	token := "tok"
	called := false
	addr, _ := startPokeServer(t, token, func(s *Server) {
		s.IdeaPromoter = func(IdeaPromoteRequest) error {
			called = true
			return nil
		}
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"idea-promote", `{}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.False(t, called)
}
