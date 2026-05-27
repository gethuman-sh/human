package chrome

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// Dialer abstracts TCP connection creation for testability.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// DefaultDialer uses the standard net.Dialer.
type DefaultDialer struct{}

// DialContext dials using the standard library.
func (DefaultDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// SocketDir returns the directory where Chrome MCP bridge sockets are created.
// It follows the same convention as the Claude MCP browser bridge:
// /tmp/claude-mcp-browser-bridge-<username>/
func SocketDir() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", errors.WrapWithDetails(err, "getting current user")
	}
	return filepath.Join(os.TempDir(), "claude-mcp-browser-bridge-"+u.Username), nil
}

// Bridge creates a fake Unix socket inside a container and tunnels traffic
// over TCP to the daemon on the host, which connects to the real Chrome
// native messaging socket.
type Bridge struct {
	Dialer  Dialer
	Addr    string // HUMAN_CHROME_ADDR (TCP address of daemon's chrome proxy)
	Token   string
	Version string
	Logger  zerolog.Logger
}

// ListenAndServe creates a Unix socket in SocketDir() and accepts connections,
// tunneling each to the daemon's chrome proxy server over TCP.
func (b *Bridge) ListenAndServe(ctx context.Context) error {
	dir, err := SocketDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating socket directory", "path", dir)
	}

	sockPath := filepath.Join(dir, fmt.Sprintf("%d.sock", os.Getpid()))

	// Narrow umask around Listen so the socket inode is created with
	// 0600 from birth. Without this there is a small TOCTOU window
	// between Listen and Chmod where another local user could connect.
	var ln net.Listener
	var listenErr error
	withRestrictiveUmask(func() {
		ln, listenErr = net.Listen("unix", sockPath)
	})
	if listenErr != nil || ln == nil {
		if listenErr == nil {
			listenErr = errors.WithDetails("net.Listen returned nil listener without error")
		}
		return errors.WrapWithDetails(listenErr, "listening on unix socket", "path", sockPath)
	}

	// Match native host socket permissions (0600) so Claude Code's
	// validateSocketSecurity accepts our socket.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		return errors.WrapWithDetails(err, "setting socket permissions", "path", sockPath)
	}

	defer func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	b.Logger.Info().Str("socket", sockPath).Str("addr", b.Addr).Msg("chrome bridge listening")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.Logger.Warn().Err(err).Msg("bridge accept error")
			continue
		}
		if conn == nil {
			continue
		}
		go b.handleConn(ctx, conn)
	}
}

// handleConn tunnels a single Unix connection to the daemon's chrome proxy.
func (b *Bridge) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	tcpConn, err := b.Dialer.DialContext(ctx, "tcp", b.Addr)
	if err != nil {
		b.Logger.Error().Err(err).Msg("failed to dial daemon")
		return
	}
	defer func() { _ = tcpConn.Close() }()

	// Authenticate with the daemon's chrome proxy.
	if err := sendProxyRequest(tcpConn, b.Token, b.Version); err != nil {
		b.Logger.Error().Err(err).Msg("failed to send proxy request")
		return
	}

	ack, err := readProxyAck(tcpConn)
	if err != nil {
		b.Logger.Error().Err(err).Msg("failed to read proxy ack")
		return
	}
	if !ack.OK {
		b.Logger.Error().Str("error", ack.Error).Msg("daemon rejected connection")
		return
	}

	// Bidirectional copy between unix socket and TCP connection.
	// Select on ctx.Done so an orderly shutdown tears down blocked
	// io.Copy calls instead of waiting indefinitely for a peer to
	// send EOF.
	errCh := make(chan error, 2) //nolint:mnd // two directions

	go func() {
		_, cpErr := io.Copy(tcpConn, conn)
		if tc, ok := tcpConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		errCh <- cpErr
	}()

	go func() {
		_, cpErr := io.Copy(conn, tcpConn)
		errCh <- cpErr
	}()

	consumed := 0
	select {
	case <-errCh:
		consumed++
	case <-ctx.Done():
		_ = conn.Close()
		_ = tcpConn.Close()
	}
	for i := consumed; i < 2; i++ {
		<-errCh
	}
}
