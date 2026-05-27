package chrome

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

const dialTimeout = 1 * time.Second

// SocketConnector implements ProcessSpawner by connecting to a Unix socket
// in the socket directory. It is used by the daemon to connect to the Chrome
// native messaging bridge socket created by the bridge command.
type SocketConnector struct {
	SocketDir string
	Logger    zerolog.Logger
}

// Spawn connects to the first reachable .sock file in SocketDir.
func (sc *SocketConnector) Spawn(_ context.Context) (io.WriteCloser, io.ReadCloser, func() error, error) {
	matches, err := filepath.Glob(filepath.Join(sc.SocketDir, "*.sock"))
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "globbing socket directory",
			"dir", sc.SocketDir)
	}

	if len(matches) == 0 {
		return nil, nil, nil, errors.WithDetails("no sockets found in %s", "dir", sc.SocketDir)
	}

	for _, path := range matches {
		conn, dErr := net.DialTimeout("unix", path, dialTimeout)
		if dErr != nil {
			sc.Logger.Debug().Str("path", path).Err(dErr).Msg("skipping stale socket")
			continue
		}

		sc.Logger.Info().Str("path", path).Msg("connected to bridge socket")

		wc := &connWriteCloser{conn: conn}
		rc := &connReadCloser{conn: conn}
		wait := func() error {
			return conn.Close()
		}
		return wc, rc, wait, nil
	}

	return nil, nil, nil, errors.WithDetails("all sockets stale in %s", "dir", sc.SocketDir)
}

// connWriteCloser wraps a net.Conn to satisfy io.WriteCloser.
// CloseWrite is called if the underlying connection supports it.
type connWriteCloser struct {
	conn net.Conn
}

func (c *connWriteCloser) Write(p []byte) (int, error) {
	return c.conn.Write(p)
}

func (c *connWriteCloser) Close() error {
	if uc, ok := c.conn.(*net.UnixConn); ok {
		return uc.CloseWrite()
	}
	return c.conn.Close()
}

// connReadCloser wraps a net.Conn to satisfy io.ReadCloser.
type connReadCloser struct {
	conn net.Conn
}

func (c *connReadCloser) Read(p []byte) (int, error) {
	return c.conn.Read(p)
}

func (c *connReadCloser) Close() error {
	return c.conn.Close()
}
