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

func startCloseServer(t *testing.T, token string, closer func(CloseTicketRequest) error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:          "127.0.0.1:0",
		Token:         token,
		CmdFactory:    echoCmd,
		Logger:        zerolog.Nop(),
		CloseTicketer: closer,
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

func TestHandleCloseTicketNilCloser(t *testing.T) {
	token := "tok"
	addr := startCloseServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"close-ticket", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "closing tickets not available")
}

func TestHandleCloseTicketValid(t *testing.T) {
	token := "tok"
	var got CloseTicketRequest
	addr := startCloseServer(t, token, func(req CloseTicketRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(CloseTicketRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"close-ticket", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "SC-1", got.PMKey)
}

func TestHandleCloseTicketBadArg(t *testing.T) {
	token := "tok"
	addr := startCloseServer(t, token, func(CloseTicketRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"close-ticket", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid close-ticket request")
}

// The close-ticket route must NOT trip the interactive destructive-confirm gate;
// the board's drag-and-confirm dialog is the consent, matching board-transition.
func TestDetectDestructiveBypassesCloseTicket(t *testing.T) {
	_, ok := detectDestructive([]string{"close-ticket", `{"pm_key":"SC-1"}`})
	assert.False(t, ok)
}
