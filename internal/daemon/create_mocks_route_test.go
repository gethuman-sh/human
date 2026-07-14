package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startCreateMocksServer(t *testing.T, token string, creator func(CreateMocksRequest) error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:           "127.0.0.1:0",
		Token:          token,
		CmdFactory:     echoCmd,
		Logger:         zerolog.Nop(),
		MockupsCreator: creator,
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

func TestHandleCreateMocksNilCreator(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "mock creation not available")
}

func TestHandleCreateMocksValid(t *testing.T) {
	token := "tok"
	var got CreateMocksRequest
	addr := startCreateMocksServer(t, token, func(req CreateMocksRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(CreateMocksRequest{PMKey: "SC-1", PMTitle: "Dark mode", Description: "ctx"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "SC-1", got.PMKey)
	assert.Equal(t, "Dark mode", got.PMTitle)
	assert.Equal(t, "ctx", got.Description)
}

func TestHandleCreateMocksBadArg(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, func(CreateMocksRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid create-mocks request")
}

func TestHandleCreateMocksMissingArg(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, func(CreateMocksRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "requires one JSON arg")
}

func TestHandleCreateMocksCreatorError(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, func(CreateMocksRequest) error {
		return errors.New("launch failed")
	})
	body, _ := json.Marshal(CreateMocksRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "launch failed")
}

// The create-mocks route must NOT trip the interactive destructive-confirm
// gate; the context-menu click is the consent, matching features-generate.
func TestDetectDestructiveBypassesCreateMocks(t *testing.T) {
	_, ok := detectDestructive([]string{"create-mocks", `{"pm_key":"SC-1"}`})
	assert.False(t, ok)
}
