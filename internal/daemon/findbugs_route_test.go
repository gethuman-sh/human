package daemon

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startFindbugsServer(t *testing.T, token string, runner func() error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:           "127.0.0.1:0",
		Token:          token,
		CmdFactory:     echoCmd,
		Logger:         zerolog.Nop(),
		FindbugsRunner: runner,
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

func TestHandleFindbugsStartNilRunner(t *testing.T) {
	token := "tok"
	addr := startFindbugsServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"findbugs-start"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "findbugs sweep not available")
}

func TestHandleFindbugsStartValid(t *testing.T) {
	token := "tok"
	called := false
	addr := startFindbugsServer(t, token, func() error {
		called = true
		return nil
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"findbugs-start"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.True(t, called, "runner should have been invoked")
}

func TestHandleFindbugsStartError(t *testing.T) {
	token := "tok"
	addr := startFindbugsServer(t, token, func() error {
		return errors.New("launch failed: docker down")
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"findbugs-start"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "launch failed: docker down")
}

// The findbugs-start route must NOT trip the interactive destructive-confirm
// gate; the Bugs pane's Findbugs button press is the user's consent, matching
// features-generate.
func TestDetectDestructiveBypassesFindbugsStart(t *testing.T) {
	_, ok := detectDestructive([]string{"findbugs-start"})
	assert.False(t, ok)
}
