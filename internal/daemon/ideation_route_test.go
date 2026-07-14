package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func startIdeationServer(t *testing.T, token string, engine *IdeationEngine) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
		Ideation:   engine,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr
}

func TestHandleIdeationStartNilEngine(t *testing.T) {
	token := "tok"
	addr := startIdeationServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-start", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "ideation not available")
}

func TestHandleIdeationStartValid(t *testing.T) {
	token := "tok"
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "Q1?", ResumeID: "cs-1"}}}
	engine := newTestEngine(runner, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)

	body, _ := json.Marshal(IdeationStartRequest{Seed: "multi word idea"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-start", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)

	var st IdeationStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.NotEmpty(t, st.SessionID)
	assert.Equal(t, IdeationThinking, st.State)
}

func TestHandleIdeationStartBadArg(t *testing.T) {
	token := "tok"
	engine := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-start", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid ideation-start request")
}

func TestHandleIdeationReplyNoSession(t *testing.T) {
	token := "tok"
	engine := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)

	body, _ := json.Marshal(IdeationReplyRequest{SessionID: "nope", Message: "hi"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-reply", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no matching ideation session")
}

func TestHandleIdeationReplyValid(t *testing.T) {
	token := "tok"
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "Q1?", ResumeID: "cs-1"},
		{Reply: "Q2?", ResumeID: "cs-2"},
	}}
	engine := newTestEngine(runner, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)

	startBody, _ := json.Marshal(IdeationStartRequest{Seed: "seed"})
	startResp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-start", string(startBody)}})
	require.Equal(t, 0, startResp.ExitCode)
	var started IdeationStatus
	require.NoError(t, json.Unmarshal([]byte(startResp.Stdout), &started))

	deadline := time.Now().Add(2 * time.Second)
	var status IdeationStatus
	for time.Now().Before(deadline) {
		statusResp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-status"}})
		require.NoError(t, json.Unmarshal([]byte(statusResp.Stdout), &status))
		if status.State == IdeationAwaitingReply {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, IdeationAwaitingReply, status.State)

	replyBody, _ := json.Marshal(IdeationReplyRequest{SessionID: started.SessionID, Message: "answer"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-reply", string(replyBody)}})
	assert.Equal(t, 0, resp.ExitCode)
	var st IdeationStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.Equal(t, IdeationThinking, st.State)
}

func TestHandleIdeationReplyNilEngine(t *testing.T) {
	token := "tok"
	addr := startIdeationServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-reply", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "ideation not available")
}

func TestHandleIdeationReplyBadArg(t *testing.T) {
	token := "tok"
	engine := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-reply", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid ideation-reply request")
}

func TestHandleIdeationStatusNilEngine(t *testing.T) {
	token := "tok"
	addr := startIdeationServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-status"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "ideation not available")
}

func TestHandleIdeationStatusEmpty(t *testing.T) {
	token := "tok"
	engine := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-status"}})
	assert.Equal(t, 0, resp.ExitCode)
	var st IdeationStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.Equal(t, IdeationNone, st.State)
}

func TestDetectDestructiveBypassesIdeation(t *testing.T) {
	_, ok := detectDestructive([]string{"ideation-start", `{"seed":"idea"}`})
	assert.False(t, ok)
}

func TestDetectDestructiveBypassesIdeationApprove(t *testing.T) {
	_, ok := detectDestructive([]string{"ideation-approve", `{"session_id":"x","title":"T"}`})
	assert.False(t, ok)
}

func TestHandleIdeationApproveNilEngine(t *testing.T) {
	token := "tok"
	addr := startIdeationServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-approve", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "ideation not available")
}

func TestHandleIdeationApproveBadArg(t *testing.T) {
	token := "tok"
	engine := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-approve", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid ideation-approve request")
}

func TestHandleIdeationApproveNoSession(t *testing.T) {
	token := "tok"
	engine := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)

	body, _ := json.Marshal(IdeationApproveRequest{SessionID: "nope", Title: "T"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-approve", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no matching ideation session")
}

func TestHandleIdeationApproveValid(t *testing.T) {
	token := "tok"
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	engine := newTestEngine(runner, newFakeCreator(), "PRJ", nil)
	addr := startIdeationServer(t, token, engine)

	startBody, _ := json.Marshal(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	startResp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-start", string(startBody)}})
	require.Equal(t, 0, startResp.ExitCode)
	var started IdeationStatus
	require.NoError(t, json.Unmarshal([]byte(startResp.Stdout), &started))

	deadline := time.Now().Add(2 * time.Second)
	var status IdeationStatus
	for time.Now().Before(deadline) {
		statusResp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-status"}})
		require.NoError(t, json.Unmarshal([]byte(statusResp.Stdout), &status))
		if status.State == IdeationAwaitingApproval {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, IdeationAwaitingApproval, status.State)

	approveBody, _ := json.Marshal(IdeationApproveRequest{SessionID: started.SessionID, Title: "edited title", Description: "edited desc"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"ideation-approve", string(approveBody)}})
	assert.Equal(t, 0, resp.ExitCode)
	var st IdeationStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.Equal(t, IdeationDone, st.State)
	assert.NotEmpty(t, st.CreatedKey)
}

func TestServer_ideaCreateRoute(t *testing.T) {
	creator := newFakeCreator()
	engine := newTestEngine(&fakeRunner{}, creator, "proj", nil)
	srv := &Server{Logger: zerolog.Nop(), Ideation: engine}

	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleIdeaCreate(conn, []string{`{"title":"Weekly digest email"}`})
	})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)
	var out IdeaCreateResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &out))
	assert.Equal(t, "SC-999", out.Key)

	captured := creator.capturedIssue()
	require.NotNil(t, captured)
	assert.Equal(t, []string{tracker.IdeaLabel}, captured.Labels)
}

func TestServer_ideaCreateRouteBadInput(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), Ideation: newTestEngine(&fakeRunner{}, newFakeCreator(), "proj", nil)}
	for name, args := range map[string][]string{
		"no arg":       {},
		"invalid json": {"{broken"},
		"empty title":  {`{"title":"  "}`},
	} {
		resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleIdeaCreate(conn, args) })
		assert.Equal(t, 1, resp.ExitCode, "case %s", name)
	}
}
