package chrome

import (
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketDir(t *testing.T) {
	dir, err := SocketDir()
	require.NoError(t, err)
	assert.Contains(t, dir, "claude-mcp-browser-bridge-")
	assert.True(t, filepath.IsAbs(dir))
}

func TestBridge_HappyPath(t *testing.T) {
	token := "bridge-test-token"
	echoData := []byte("echo-response")

	// Start a mock daemon chrome proxy server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read auth request.
		ack, rErr := handleMockAuth(conn, token)
		if rErr != nil || !ack {
			return
		}

		// Echo: read from conn, write back.
		data, _ := io.ReadAll(conn)
		_ = data
		_, _ = conn.Write(echoData)
	}()

	// Create bridge with a temp socket dir.
	dir := t.TempDir()
	ctx := t.Context()

	bridge := &Bridge{
		Dialer:  DefaultDialer{},
		Addr:    ln.Addr().String(),
		Token:   token,
		Version: "test",
		Logger:  zerolog.Nop(),
	}

	// Override SocketDir by directly creating the socket.
	sockPath := filepath.Join(dir, "test.sock")
	unixLn, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	go func() {
		conn, aErr := unixLn.Accept()
		if aErr != nil {
			return
		}
		bridge.handleConn(ctx, conn)
	}()

	// Connect to the bridge socket.
	unixConn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)

	_, err = unixConn.Write([]byte("client-data"))
	require.NoError(t, err)
	_ = unixConn.(*net.UnixConn).CloseWrite()

	got, err := io.ReadAll(unixConn)
	require.NoError(t, err)
	assert.Equal(t, echoData, got)
	_ = unixConn.Close()
	_ = unixLn.Close()
}

func TestBridge_AuthRejection(t *testing.T) {
	// Start a mock daemon that rejects auth.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		handleMockAuth(conn, "correct-token") //nolint:errcheck // test helper
	}()

	dir := t.TempDir()
	ctx := t.Context()

	bridge := &Bridge{
		Dialer:  DefaultDialer{},
		Addr:    ln.Addr().String(),
		Token:   "wrong-token",
		Version: "test",
		Logger:  zerolog.Nop(),
	}

	sockPath := filepath.Join(dir, "test.sock")
	unixLn, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer func() { _ = unixLn.Close() }()

	done := make(chan struct{})
	go func() {
		conn, aErr := unixLn.Accept()
		if aErr != nil {
			return
		}
		bridge.handleConn(ctx, conn)
		close(done)
	}()

	unixConn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)

	// Should get EOF because bridge closes conn after rejection.
	_, err = io.ReadAll(unixConn)
	require.NoError(t, err)
	_ = unixConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not return after auth rejection")
	}
}

func TestBridge_DialFailure(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()

	bridge := &Bridge{
		Dialer:  DefaultDialer{},
		Addr:    "127.0.0.1:1", // unreachable
		Token:   "tok",
		Version: "test",
		Logger:  zerolog.Nop(),
	}

	sockPath := filepath.Join(dir, "test.sock")
	unixLn, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer func() { _ = unixLn.Close() }()

	done := make(chan struct{})
	go func() {
		conn, aErr := unixLn.Accept()
		if aErr != nil {
			return
		}
		bridge.handleConn(ctx, conn)
		close(done)
	}()

	unixConn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	_ = unixConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not return after dial failure")
	}
}

// handleMockAuth reads a proxy request (JSON line) and sends an ack.
// Returns (true, nil) if auth succeeded, (false, nil) if auth failed.
func handleMockAuth(conn net.Conn, expectedToken string) (bool, error) {
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return false, err
	}

	var req proxyRequest
	if err := json.Unmarshal(buf[:n], &req); err != nil {
		return false, err
	}

	ok := req.Token == expectedToken
	resp := ProxyAck{OK: ok}
	if !ok {
		resp.Error = "authentication failed: invalid token"
	}
	respBytes, _ := json.Marshal(resp)
	respBytes = append(respBytes, '\n')
	_, err = conn.Write(respBytes)
	return ok, err
}
