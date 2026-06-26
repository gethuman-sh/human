package monarch

import (
	"bufio"
	"context"
	"encoding/json"
	"net"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// monarchMaxConns caps concurrent daemon connections so a flood of connecting
// daemons cannot exhaust file descriptors.
const monarchMaxConns = 256

// scanBufMax bounds a single wire line to 1 MiB, matching the established cap
// elsewhere in the codebase, so a malformed peer cannot force unbounded
// allocation per line.
const scanBufMax = 1 << 20

// Server is the monarch-side TCP listener. It reads newline-delimited JSON
// Events from daemon connections and writes them to the Store. No auth (MVP).
type Server struct {
	Addr   string
	Store  *Store
	Logger zerolog.Logger
}

// ListenAndServe accepts daemon connections until ctx is cancelled. Each conn is
// a long-lived stream of newline-delimited JSON Events.
func (s *Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.Addr)
	if err != nil {
		return errors.WrapWithDetails(err, "monarch listen failed", "addr", s.Addr)
	}
	defer func() { _ = ln.Close() }()

	s.Logger.Info().Str("addr", ln.Addr().String()).Msg("monarch ingest listening")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	sem := make(chan struct{}, monarchMaxConns)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn().Err(err).Msg("monarch accept error")
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
			s.Logger.Warn().Msg("monarch connection limit reached, rejecting")
			_ = conn.Close()
		}
	}
}

// handleConn reads one JSON Event per line and inserts each. A malformed line is
// logged and skipped, never fatal. Returns on EOF or ctx cancel.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), scanBufMax)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			s.Logger.Debug().Err(err).Msg("monarch skipping malformed event line")
			continue
		}
		if err := s.Store.Insert(ctx, e); err != nil {
			s.Logger.Warn().Err(err).Msg("monarch failed to persist event")
		}
	}
}
