package chrome

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketConnector_ConnectToSocket(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "123.sock")

	// Create a real Unix socket listener.
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	echoData := []byte("hello from native host")
	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read input, then write response.
		data, _ := io.ReadAll(conn)
		_ = data
		_, _ = conn.Write(echoData)
	}()

	sc := &SocketConnector{
		SocketDir: dir,
		Logger:    zerolog.Nop(),
	}

	stdin, stdout, wait, err := sc.Spawn(context.Background())
	require.NoError(t, err)

	_, err = stdin.Write([]byte("test"))
	require.NoError(t, err)
	require.NoError(t, stdin.Close())

	got, err := io.ReadAll(stdout)
	require.NoError(t, err)
	assert.Equal(t, echoData, got)

	err = wait()
	assert.NoError(t, err)
}

func TestSocketConnector_SkipStaleSocket(t *testing.T) {
	dir := shortTempDir(t)

	// Create a stale socket file (not listening).
	stalePath := filepath.Join(dir, "stale.sock")
	require.NoError(t, os.WriteFile(stalePath, []byte{}, 0o600))

	// Create a live socket.
	livePath := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", livePath)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		_ = conn.Close()
	}()

	sc := &SocketConnector{
		SocketDir: dir,
		Logger:    zerolog.Nop(),
	}

	stdin, stdout, wait, err := sc.Spawn(context.Background())
	require.NoError(t, err)
	_ = stdin.Close()
	_ = stdout.Close()
	_ = wait()
}

func TestSocketConnector_EmptyDir(t *testing.T) {
	dir := shortTempDir(t)

	sc := &SocketConnector{
		SocketDir: dir,
		Logger:    zerolog.Nop(),
	}

	_, _, _, err := sc.Spawn(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sockets found")
}

func TestSocketConnector_AllStale(t *testing.T) {
	dir := shortTempDir(t)

	// Create stale socket files (not listening).
	for _, name := range []string{"a.sock", "b.sock"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte{}, 0o600))
	}

	sc := &SocketConnector{
		SocketDir: dir,
		Logger:    zerolog.Nop(),
	}

	_, _, _, err := sc.Spawn(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all sockets stale")
}
