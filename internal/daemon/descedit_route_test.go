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
)

func startDescEditServer(t *testing.T, token string, engine *DescEditEngine) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
		DescEdit:   engine,
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

func TestHandleDescEditStartNilEngine(t *testing.T) {
	token := "tok"
	addr := startDescEditServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-start", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "description edit not available")
}

func TestHandleDescEditStartValid(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)

	body, _ := json.Marshal(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-start", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)

	var st DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.NotEmpty(t, st.SessionID)
	assert.Equal(t, DescEditAwaitingReply, st.State)
}

func TestHandleDescEditStartBadArg(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-start", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid descedit-start request")
}

func TestHandleDescEditReplyNoSession(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)

	body, _ := json.Marshal(DescEditReplyRequest{SessionID: "nope", Message: "hi"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-reply", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no matching description-edit session")
}

func TestHandleDescEditReplyValid(t *testing.T) {
	token := "tok"
	runner := &fakeRunner{turns: []ChatTurn{{Reply: "ok, thinking...", ResumeID: "cs-1"}}}
	engine := newTestDescEditEngine(runner, nil)
	addr := startDescEditServer(t, token, engine)

	startBody, _ := json.Marshal(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	startResp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-start", string(startBody)}})
	require.Equal(t, 0, startResp.ExitCode)
	var started DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(startResp.Stdout), &started))

	replyBody, _ := json.Marshal(DescEditReplyRequest{SessionID: started.SessionID, Message: "rewrite it"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-reply", string(replyBody)}})
	assert.Equal(t, 0, resp.ExitCode)
	var st DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.Equal(t, DescEditThinking, st.State)
}

func TestHandleDescEditReplyNilEngine(t *testing.T) {
	token := "tok"
	addr := startDescEditServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-reply", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "description edit not available")
}

func TestHandleDescEditReplyBadArg(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-reply", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid descedit-reply request")
}

func TestHandleDescEditStatusNilEngine(t *testing.T) {
	token := "tok"
	addr := startDescEditServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-status"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "description edit not available")
}

func TestHandleDescEditStatusEmpty(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-status"}})
	assert.Equal(t, 0, resp.ExitCode)
	var st DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &st))
	assert.Equal(t, DescEditNone, st.State)
}

func TestHandleDescEditApplyNilEngine(t *testing.T) {
	token := "tok"
	addr := startDescEditServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-apply", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "description edit not available")
}

func TestHandleDescEditApplyBadArg(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-apply", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid descedit-apply request")
}

func TestHandleDescEditApplyNoSession(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)

	body, _ := json.Marshal(DescEditApplyRequest{SessionID: "nope"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-apply", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no matching description-edit session")
}

func TestHandleDescEditDiscardNilEngine(t *testing.T) {
	token := "tok"
	addr := startDescEditServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-discard", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "description edit not available")
}

func TestHandleDescEditDiscardBadArg(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-discard", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid descedit-discard request")
}

// TestHandleDescEditDiscardValid covers the AC6 route end to end: Discard
// ends the matching session, and Status confirms None afterward.
func TestHandleDescEditDiscardValid(t *testing.T) {
	token := "tok"
	engine := newTestDescEditEngine(&fakeRunner{}, nil)
	addr := startDescEditServer(t, token, engine)

	startBody, _ := json.Marshal(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	startResp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-start", string(startBody)}})
	require.Equal(t, 0, startResp.ExitCode)
	var started DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(startResp.Stdout), &started))

	discardBody, _ := json.Marshal(DescEditDiscardRequest{SessionID: started.SessionID})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-discard", string(discardBody)}})
	assert.Equal(t, 0, resp.ExitCode)
	var discarded DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &discarded))
	assert.Equal(t, DescEditNone, discarded.State)

	statusResp := sendRequest(t, addr, Request{Token: token, Args: []string{"descedit-status"}})
	var status DescEditStatus
	require.NoError(t, json.Unmarshal([]byte(statusResp.Stdout), &status))
	assert.Equal(t, DescEditNone, status.State)
}

func TestDetectDestructiveBypassesDescEdit(t *testing.T) {
	_, ok := detectDestructive([]string{"descedit-start", `{"key":"SC-1"}`})
	assert.False(t, ok)
}

func TestDetectDestructiveBypassesDescEditApply(t *testing.T) {
	_, ok := detectDestructive([]string{"descedit-apply", `{"session_id":"x"}`})
	assert.False(t, ok)
}

func TestDetectDestructiveBypassesDescEditDiscard(t *testing.T) {
	_, ok := detectDestructive([]string{"descedit-discard", `{"session_id":"x"}`})
	assert.False(t, ok)
}
