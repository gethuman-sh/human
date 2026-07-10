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

func startFeaturesServer(t *testing.T, token string, generator func() error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:              "127.0.0.1:0",
		Token:             token,
		CmdFactory:        echoCmd,
		Logger:            zerolog.Nop(),
		FeaturesGenerator: generator,
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

func TestHandleFeaturesGenerateNilGenerator(t *testing.T) {
	token := "tok"
	addr := startFeaturesServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"features-generate"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "feature generation not available")
}

func TestHandleFeaturesGenerateValid(t *testing.T) {
	token := "tok"
	called := false
	addr := startFeaturesServer(t, token, func() error {
		called = true
		return nil
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"features-generate"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.True(t, called, "generator should have been invoked")
}

func TestHandleFeaturesGenerateError(t *testing.T) {
	token := "tok"
	addr := startFeaturesServer(t, token, func() error {
		return errors.New("launch failed: docker down")
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"features-generate"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "launch failed: docker down")
}

// The features-generate route must NOT trip the interactive destructive-confirm
// gate; the pane's Generate/Refresh button press is the user's consent, matching
// board-transition.
func TestDetectDestructiveBypassesFeaturesGenerate(t *testing.T) {
	_, ok := detectDestructive([]string{"features-generate"})
	assert.False(t, ok)
}
