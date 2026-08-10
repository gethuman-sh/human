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

	"github.com/gethuman-sh/human/errors"
)

func startRelateServer(t *testing.T, token string, launcher func(RelateRequest) error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:           "127.0.0.1:0",
		Token:          token,
		CmdFactory:     echoCmd,
		Logger:         zerolog.Nop(),
		RelateLauncher: launcher,
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

func TestValidateRelate(t *testing.T) {
	assert.Error(t, ValidateRelate(RelateRequest{PMKey: "   "}), "empty pm_key is rejected")
	assert.NoError(t, ValidateRelate(RelateRequest{PMKey: "SC-1"}))
}

func TestHandleRelate_launcherNil(t *testing.T) {
	token := "tok"
	addr := startRelateServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"relate", `{"pm_key":"SC-1"}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "related-work triage not available")
}

func TestHandleRelate_dispatches(t *testing.T) {
	token := "tok"
	var got RelateRequest
	addr := startRelateServer(t, token, func(req RelateRequest) error {
		got = req
		return nil
	})

	body, _ := json.Marshal(RelateRequest{PMKey: "SC-42", PMTitle: "Board loses cards"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"relate", string(body)}})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)
	// The one-JSON-arg protocol keeps the multi-word title intact for the launch.
	assert.Equal(t, "SC-42", got.PMKey)
	assert.Equal(t, "Board loses cards", got.PMTitle)
}

func TestHandleRelate_launcherError(t *testing.T) {
	token := "tok"
	addr := startRelateServer(t, token, func(RelateRequest) error {
		return errors.WithDetails("no docker")
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"relate", `{"pm_key":"SC-1"}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no docker")
}

func TestHandleRelate_emptyKey(t *testing.T) {
	token := "tok"
	addr := startRelateServer(t, token, func(RelateRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"relate", `{"pm_key":"  "}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "needs a pm_key")
}

func TestHandleRelate_badJSON(t *testing.T) {
	token := "tok"
	addr := startRelateServer(t, token, func(RelateRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"relate", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid relate request")
}

func TestHandleRelate_wrongArgCount(t *testing.T) {
	token := "tok"
	addr := startRelateServer(t, token, func(RelateRequest) error { return nil })
	for name, args := range map[string][]string{
		"no arg":   {"relate"},
		"two args": {"relate", "{}", "{}"},
	} {
		resp := sendRequest(t, addr, Request{Token: token, Args: args})
		assert.Equal(t, 1, resp.ExitCode, name)
		assert.Contains(t, resp.Stderr, "relate requires one JSON arg", name)
	}
}

func TestRelateClient_roundtrip(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		require.Len(t, req.Args, 2)
		assert.Equal(t, "relate", req.Args[0])
		var got RelateRequest
		require.NoError(t, json.Unmarshal([]byte(req.Args[1]), &got))
		assert.Equal(t, "SC-7", got.PMKey)
		assert.Equal(t, "flaky", got.PMTitle)
		return Response{ExitCode: 0}
	})

	err := newTestClient(addr, "tok").Relate(RelateRequest{PMKey: "SC-7", PMTitle: "flaky"})
	require.NoError(t, err)
}

func TestRelateClient_errorPropagates(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{ExitCode: 1, Stderr: "related-work triage not available"}
	})
	err := newTestClient(addr, "tok").Relate(RelateRequest{PMKey: "SC-7"})
	require.Error(t, err)
}
