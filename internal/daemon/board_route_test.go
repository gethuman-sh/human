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

func startBoardServer(t *testing.T, token string, transitioner func(BoardTransitionRequest) error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:              "127.0.0.1:0",
		Token:             token,
		CmdFactory:        echoCmd,
		Logger:            zerolog.Nop(),
		BoardTransitioner: transitioner,
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

func TestHandleBoardTransitionNilTransitioner(t *testing.T) {
	token := "tok"
	addr := startBoardServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-transition", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "board transitions not available")
}

func TestHandleBoardTransitionValid(t *testing.T) {
	token := "tok"
	var got BoardTransitionRequest
	addr := startBoardServer(t, token, func(req BoardTransitionRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(BoardTransitionRequest{PMKey: "SC-1", PMTitle: "multi word title", To: BoardPlanning})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-transition", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "SC-1", got.PMKey)
	assert.Equal(t, "multi word title", got.PMTitle)
}

func TestHandleBoardTransitionBadArg(t *testing.T) {
	token := "tok"
	addr := startBoardServer(t, token, func(BoardTransitionRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-transition", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid board-transition request")
}

func TestDetectDestructiveBypassesBoardTransition(t *testing.T) {
	_, ok := detectDestructive([]string{"board-transition", `{"pm_key":"SC-1","to":"done"}`})
	assert.False(t, ok)
}

func startBoardFixServer(t *testing.T, token string, fixer func(BoardFixRequest) error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
		BoardFixer: fixer,
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

func TestHandleBoardFixNilFixer(t *testing.T) {
	token := "tok"
	addr := startBoardFixServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-fix", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "bug fixing not available")
}

func TestHandleBoardFixValid(t *testing.T) {
	token := "tok"
	var got BoardFixRequest
	addr := startBoardFixServer(t, token, func(req BoardFixRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(BoardFixRequest{PMKey: "SC-9", PMTitle: "crash on save"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-fix", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "SC-9", got.PMKey)
	assert.Equal(t, "crash on save", got.PMTitle)
}

func TestHandleBoardFixBadArg(t *testing.T) {
	token := "tok"
	addr := startBoardFixServer(t, token, func(BoardFixRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-fix", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid board-fix request")
}

func TestDetectDestructiveBypassesBoardFix(t *testing.T) {
	_, ok := detectDestructive([]string{"board-fix", `{"pm_key":"SC-9"}`})
	assert.False(t, ok)
}
