package proxy

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// proxyMaxConns caps concurrent in-flight HTTPS proxy connections.
const proxyMaxConns = 256

// proxyHelloDeadline bounds the time the proxy waits for the TLS
// ClientHello before closing an idle connection.
const proxyHelloDeadline = 10 * time.Second

// proxyDialTimeout bounds upstream connection establishment.
const proxyDialTimeout = 30 * time.Second

// Server is a transparent HTTPS proxy that reads the SNI from TLS ClientHello
// to block/allow domains without decrypting traffic. Domains listed in the
// Interceptor are MITM'd for traffic inspection/logging.
type Server struct {
	Addr        string
	Policy      Decider
	Interceptor Interceptor // optional: MITM interceptor for specific domains
	Logger      zerolog.Logger
	// Emitter records ambient network decisions for the TUI activity
	// panel. Optional — nil means no events are recorded so tests and
	// standalone use pay nothing for the feature.
	Emitter NetworkEventEmitter
	// Dialer connects to upstream servers. Injected for testing.
	Dialer func(ctx context.Context, network, address string) (net.Conn, error)

	activeConns atomic.Int64 // number of currently-active forwarded connections
}

// emit safely forwards a network decision to the configured emitter.
// A nil Emitter is a valid configuration (e.g. tests, standalone use)
// and is silently ignored so the hot path pays no cost when unused.
func (s *Server) emit(source, status, host string) {
	if s.Emitter != nil {
		s.Emitter.Emit(source, status, host)
	}
}

// ActiveConns returns the number of currently active forwarded connections.
func (s *Server) ActiveConns() int64 {
	return s.activeConns.Load()
}

// ListenAndServe starts the TCP listener and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.Addr)
	if err != nil {
		return errors.WrapWithDetails(err, "https proxy listen failed",
			"addr", s.Addr)
	}
	closeOnce := sync.Once{}
	closeLn := func() { closeOnce.Do(func() { _ = ln.Close() }) }
	defer closeLn()

	s.Logger.Info().Str("addr", ln.Addr().String()).Msg("https proxy listening")

	go func() {
		<-ctx.Done()
		closeLn()
	}()

	sem := make(chan struct{}, proxyMaxConns)

	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn().Err(acceptErr).Msg("https proxy accept error")
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
			s.Logger.Warn().Msg("https proxy connection limit reached, rejecting")
			_ = conn.Close()
		}
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Bound the wait for the TLS ClientHello so a stalled connection
	// can't sit on a goroutine forever.
	_ = conn.SetReadDeadline(time.Now().Add(proxyHelloDeadline))

	peeked, serverName, err := PeekClientHello(conn)
	if err != nil {
		s.Logger.Debug().Err(err).Msg("SNI extraction failed")
		s.emit("fail", "parse-fail", "")
		return
	}

	// Hello complete — clear the deadline so the long-lived forwarded
	// stream isn't subject to it.
	_ = conn.SetReadDeadline(time.Time{})

	if serverName == "" {
		s.Logger.Debug().Msg("no SNI in ClientHello, blocking")
		s.emit("fail", "no-sni", "")
		return
	}

	if !s.Policy.Allowed(serverName) {
		s.Logger.Info().Str("host", serverName).Msg("blocked by policy")
		s.emit("proxy", "block", serverName)
		return
	}

	// Check if this domain should be intercepted (MITM for logging/inspection).
	if s.Interceptor != nil && s.Interceptor.ShouldIntercept(serverName) {
		s.activeConns.Add(1)
		defer s.activeConns.Add(-1)

		s.Logger.Info().Str("host", serverName).Msg("intercepting (MITM)")
		s.emit("proxy", "intercept", serverName)
		if interceptErr := s.Interceptor.Intercept(ctx, conn, serverName, peeked); interceptErr != nil {
			s.Logger.Warn().Err(interceptErr).Str("host", serverName).Msg("intercept failed")
		}
		return
	}

	dialer := s.dialer()
	upstream, err := dialer(ctx, "tcp", net.JoinHostPort(serverName, "443"))
	if err != nil {
		// A misbehaving injected dialer may return both a non-nil
		// connection and an error. Close the connection so it does
		// not leak past the failure path.
		if upstream != nil {
			_ = upstream.Close()
		}
		s.Logger.Warn().Err(err).Str("host", serverName).Msg("upstream dial failed")
		s.emit("fail", "dial-fail", serverName)
		return
	}

	s.activeConns.Add(1)
	defer s.activeConns.Add(-1)

	s.Logger.Info().Str("host", serverName).Msg("forwarding")
	s.emit("proxy", "forward", serverName)
	Forward(ctx, conn, upstream, peeked, s.Logger)
}

func (s *Server) dialer() func(ctx context.Context, network, address string) (net.Conn, error) {
	if s.Dialer != nil {
		return s.Dialer
	}
	d := &net.Dialer{Timeout: proxyDialTimeout}
	return d.DialContext
}
