package daemon

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startDockerServer starts a server with the given DockerProbe and returns its
// address, mirroring startBoardServer for the docker-available route.
func startDockerServer(t *testing.T, token string, probe func() bool) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:        "127.0.0.1:0",
		Token:       token,
		CmdFactory:  echoCmd,
		Logger:      zerolog.Nop(),
		DockerProbe: probe,
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

func TestHandleDockerAvailable_probeTrue(t *testing.T) {
	token := "tok"
	addr := startDockerServer(t, token, func() bool { return true })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"docker-available"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "true\n", resp.Stdout)
}

func TestHandleDockerAvailable_probeFalse(t *testing.T) {
	token := "tok"
	addr := startDockerServer(t, token, func() bool { return false })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"docker-available"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "false\n", resp.Stdout)
}

func TestHandleDockerAvailable_nilProbe(t *testing.T) {
	token := "tok"
	// nil DockerProbe must fail safe (false) and not panic.
	addr := startDockerServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"docker-available"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "false\n", resp.Stdout)
}

func TestDockerAvailable_notDestructive(t *testing.T) {
	// A read-only verb must bypass the destructive-confirm gate.
	_, ok := detectDestructive([]string{"docker-available"})
	assert.False(t, ok)
}
