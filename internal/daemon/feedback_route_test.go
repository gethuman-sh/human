package daemon

import (
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func feedbackRequest(t *testing.T, srv *Server, req FeedbackRequest) Response {
	t.Helper()
	arg, err := json.Marshal(req)
	require.NoError(t, err)
	return captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleFeedback(conn, []string{string(arg)})
	})
}

func TestFeedbackRoute_AnswersWithTheReport(t *testing.T) {
	var got FeedbackRequest
	srv := &Server{Logger: zerolog.Nop(), Feedback: func(req FeedbackRequest) (FeedbackReport, error) {
		got = req
		return FeedbackReport{Key: req.Key, Stage: req.Stage, Rows: 3, Block: "- a.go [tests]: add one", Prompt: "the prompt"}, nil
	}}

	resp := feedbackRequest(t, srv, FeedbackRequest{Key: "SC-1", Stage: BoardImplementation})

	require.Equal(t, 0, resp.ExitCode, resp.Stderr)
	var report FeedbackReport
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &report))
	assert.Equal(t, "SC-1", got.Key)
	assert.Equal(t, BoardImplementation, report.Stage)
	assert.Equal(t, "- a.go [tests]: add one", report.Block)
	assert.Empty(t, report.Prompt, "the prompt travels only when asked for")

	resp = feedbackRequest(t, srv, FeedbackRequest{Key: "SC-1", Stage: BoardImplementation, Raw: true})
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &report))
	assert.Equal(t, "the prompt", report.Prompt)
}

// The stage names a launch, and only the stages the daemon launches with a
// briefing can be asked about — anything else would be a briefing no launch
// ever carries.
func TestFeedbackRoute_RefusesAStageNoLaunchBriefs(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), Feedback: func(FeedbackRequest) (FeedbackReport, error) {
		t.Fatal("must not be asked")
		return FeedbackReport{}, nil
	}}

	resp := feedbackRequest(t, srv, FeedbackRequest{Key: "SC-1", Stage: BoardBacklog})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "backlog")
	assert.Contains(t, resp.Stderr, "prreview", "the refusal lists what is accepted")
}

func TestFeedbackRoute_NeedsAKeyAndSaysWhenDisabled(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), Feedback: func(FeedbackRequest) (FeedbackReport, error) {
		return FeedbackReport{}, nil
	}}
	resp := feedbackRequest(t, srv, FeedbackRequest{Stage: BoardPlanning})
	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "ticket key")

	off := &Server{Logger: zerolog.Nop()}
	resp = feedbackRequest(t, off, FeedbackRequest{Key: "SC-1", Stage: BoardPlanning})
	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "disabled")
}

func TestFeedbackRoute_PassesTheExplainerErrorThrough(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), Feedback: func(FeedbackRequest) (FeedbackReport, error) {
		return FeedbackReport{}, errors.New("no project registered for key SC-1")
	}}

	resp := feedbackRequest(t, srv, FeedbackRequest{Key: "SC-1", Stage: BoardPlanning})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no project registered")
}
