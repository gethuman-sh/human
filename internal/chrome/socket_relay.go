package chrome

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

const pendingBuffer = 16

// SocketRelay implements ProcessSpawner by creating a Unix socket and accepting
// connections from Chrome's native messaging host. When Spawn is called (by
// ForwardProxy via chrome.Server), it dequeues a waiting Chrome connection and
// returns it as stdin/stdout, pairing it with the bridge connection.
type SocketRelay struct {
	SocketDir string
	Logger    zerolog.Logger
	pending   chan net.Conn

	// mu guards the listening socket so Retire can close it from another
	// goroutine (the daemon's self-restart) while ListenAndServe is accepting.
	mu       sync.Mutex
	ln       net.Listener
	sockPath string
	retired  bool

	// activeConns counts Chrome connections currently queued or paired, so a
	// self-restart handover can drain them instead of cutting them off.
	activeConns atomic.Int64
}

// NewSocketRelay creates a SocketRelay with a buffered pending channel.
func NewSocketRelay(socketDir string, logger zerolog.Logger) *SocketRelay {
	return &SocketRelay{
		SocketDir: socketDir,
		Logger:    logger,
		pending:   make(chan net.Conn, pendingBuffer),
	}
}

// ActiveConns reports how many Chrome connections this relay currently holds
// (queued awaiting a bridge, or paired with one).
func (r *SocketRelay) ActiveConns() int64 {
	return r.activeConns.Load()
}

// Retire stops this relay from accepting and removes its socket file, without
// disturbing connections it already handed out.
//
// It exists for the daemon's self-restart: the relay socket is named after the
// process id, and clients discover one by globbing the directory and taking the
// first that answers. While both the outgoing and incoming daemon are alive,
// two sockets are discoverable and a client can pick the dying one. Retiring the
// outgoing socket the moment the successor is ready leaves exactly one.
func (r *SocketRelay) Retire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return
	}
	r.retired = true
	if r.ln != nil {
		_ = r.ln.Close()
	}
	if r.sockPath != "" {
		_ = os.Remove(r.sockPath)
	}
	r.Logger.Info().Str("path", r.sockPath).Msg("socket relay retired; successor owns the socket directory")
}

// ListenAndServe creates a Unix socket in SocketDir and accepts connections,
// queuing them in the pending channel. It blocks until ctx is cancelled.
func (r *SocketRelay) ListenAndServe(ctx context.Context) error {
	if err := os.MkdirAll(r.SocketDir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating socket directory",
			"dir", r.SocketDir)
	}

	sockPath := filepath.Join(r.SocketDir, fmt.Sprintf("%d.sock", os.Getpid()))

	// Remove stale socket file if it exists.
	_ = os.Remove(sockPath)

	// Match the bridge: narrow umask around Listen so the socket
	// inode is born 0600 instead of relying on the 0700 parent dir
	// alone. Chmod follows as defence in depth.
	var ln net.Listener
	var listenErr error
	withRestrictiveUmask(func() {
		ln, listenErr = net.Listen("unix", sockPath)
	})
	if listenErr != nil || ln == nil {
		if listenErr == nil {
			listenErr = errors.WithDetails("net.Listen returned nil listener without error")
		}
		return errors.WrapWithDetails(listenErr, "socket relay listen failed",
			"path", sockPath)
	}
	if chmodErr := os.Chmod(sockPath, 0o600); chmodErr != nil {
		_ = ln.Close()
		return errors.WrapWithDetails(chmodErr, "socket relay chmod failed",
			"path", sockPath)
	}
	// Publish the listener so Retire can close it from the handover path. A
	// relay retired before it finished binding closes immediately rather than
	// serving a socket nobody will clean up.
	r.mu.Lock()
	if r.retired {
		r.mu.Unlock()
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil
	}
	r.ln, r.sockPath = ln, sockPath
	r.mu.Unlock()

	defer func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	r.Logger.Info().Str("path", sockPath).Msg("socket relay listening")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, aErr := ln.Accept()
		if aErr != nil {
			if ctx.Err() != nil {
				r.drainPending()
				return nil
			}
			r.Logger.Warn().Err(aErr).Msg("socket relay accept error")
			continue
		}
		if conn == nil {
			continue // satisfy nilaway; Accept never returns nil without error
		}
		r.Logger.Debug().Msg("chrome native host connected to relay")

		select {
		case <-ctx.Done():
			_ = conn.Close()
			r.drainPending()
			return nil
		case r.pending <- conn:
			r.activeConns.Add(1)
		default:
			// Drop new connections when the pending queue is full
			// rather than blocking the Accept loop, which would
			// otherwise freeze the whole relay when Chrome
			// reconnects rapidly (self-DoS).
			r.Logger.Warn().Msg("socket relay pending queue full, dropping connection")
			_ = conn.Close()
		}
	}
}

// Spawn implements ProcessSpawner. It blocks until a Chrome native messaging
// connection is available (or ctx is cancelled) and returns it as stdin/stdout.
func (r *SocketRelay) Spawn(ctx context.Context) (io.WriteCloser, io.ReadCloser, func() error, error) {
	select {
	case conn := <-r.pending:
		r.Logger.Info().Msg("paired chrome connection with bridge")
		wc := &connWriteCloser{conn: conn}
		rc := &connReadCloser{conn: conn}
		// The connection stays counted until the pairing ends, so a handover
		// drain waits for live Chrome traffic rather than cutting it off. Once
		// guards against a double decrement if wait is called more than once.
		var done sync.Once
		wait := func() error {
			err := conn.Close()
			done.Do(func() { r.activeConns.Add(-1) })
			return err
		}
		return wc, rc, wait, nil
	case <-ctx.Done():
		return nil, nil, nil, errors.WrapWithDetails(ctx.Err(), "waiting for chrome connection")
	}
}

// drainPending closes all queued connections.
func (r *SocketRelay) drainPending() {
	for {
		select {
		case conn := <-r.pending:
			if conn != nil {
				_ = conn.Close()
			}
			r.activeConns.Add(-1)
		default:
			return
		}
	}
}
