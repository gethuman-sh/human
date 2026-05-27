package chrome

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// chromeMaxConns caps concurrent chrome-proxy sessions so a flood of
// connecting clients cannot exhaust file descriptors.
const chromeMaxConns = 32

// chromeAuthDeadline bounds the time the server will wait for the
// initial auth line before closing the connection.
const chromeAuthDeadline = 5 * time.Second

// Server listens for chrome-proxy connections on its own TCP port.
type Server struct {
	Addr       string
	Token      string
	Translator *McpTranslator
	Logger     zerolog.Logger
}

// ListenAndServe starts the TCP listener and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.Addr)
	if err != nil {
		return errors.WrapWithDetails(err, "chrome proxy listen failed",
			"addr", s.Addr)
	}
	defer func() { _ = ln.Close() }()

	s.Logger.Info().Str("addr", ln.Addr().String()).Msg("chrome proxy listening")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	sem := make(chan struct{}, chromeMaxConns)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn().Err(err).Msg("chrome proxy accept error")
			continue
		}
		if conn == nil {
			continue
		}
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				s.handleConn(ctx, conn)
			}()
		default:
			s.Logger.Warn().Msg("chrome proxy connection limit reached, rejecting")
			_ = conn.Close()
		}
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Bound the time we wait for the initial auth line.
	_ = conn.SetReadDeadline(time.Now().Add(chromeAuthDeadline))

	// Read the auth request (single JSON line).
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		s.writeAck(conn, false, "failed to read request")
		return
	}

	// Clear the deadline once auth is parsed; the translator below runs
	// long-lived sessions and must not inherit the auth-line deadline.
	_ = conn.SetReadDeadline(time.Time{})

	var req proxyRequest
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		s.writeAck(conn, false, "invalid request JSON")
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.Token)) != 1 {
		s.writeAck(conn, false, "authentication failed: invalid token")
		return
	}

	s.Logger.Info().Msg("starting chrome-proxy session")

	s.writeAck(conn, true, "")

	if err := s.Translator.Serve(ctx, conn); err != nil {
		s.Logger.Warn().Err(err).Msg("chrome proxy error")
	}
}

func (s *Server) writeAck(conn net.Conn, ok bool, errMsg string) {
	ack := ProxyAck{OK: ok, Error: errMsg}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(ack)
}
