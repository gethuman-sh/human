package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_allowedConnectionForwards(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"allowed.example.com"})
	require.NoError(t, err)

	// Start a mock upstream that echoes back "OK".
	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstreamLn.Close() }()

	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read the replayed ClientHello, then write response.
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte("UPSTREAM_OK"))
	}()

	ctx := t.Context()

	srv := &Server{
		Addr:   "127.0.0.1:0",
		Policy: policy,
		Logger: zerolog.Nop(),
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("tcp", upstreamLn.Addr().String())
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	// Give server time to start.
	time.Sleep(50 * time.Millisecond)

	// Connect and send a ClientHello with allowed SNI.
	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	hello := buildClientHello("allowed.example.com")
	_, err = conn.Write(hello)
	require.NoError(t, err)

	// Read upstream response.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "UPSTREAM_OK", string(buf[:n]))
}

func TestServer_blockedConnectionResets(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"allowed.example.com"})
	require.NoError(t, err)

	ctx := t.Context()

	dialed := make(chan struct{}, 1)

	srv := &Server{
		Addr:   "127.0.0.1:0",
		Policy: policy,
		Logger: zerolog.Nop(),
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			dialed <- struct{}{}
			return nil, net.UnknownNetworkError("should not be called")
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Connect with a blocked SNI.
	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	hello := buildClientHello("blocked.example.com")
	_, err = conn.Write(hello)
	require.NoError(t, err)

	// Connection should be closed by server (blocked).
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.ErrorIs(t, err, io.EOF)

	// Verify dialer was never called.
	select {
	case <-dialed:
		t.Fatal("dialer should not have been called for blocked domain")
	default:
	}
}

func TestServer_noSNIBlocks(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"*.example.com"})
	require.NoError(t, err)

	ctx := t.Context()

	srv := &Server{
		Addr:   "127.0.0.1:0",
		Policy: policy,
		Logger: zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Send a ClientHello without SNI.
	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	hello := buildClientHello("")
	_, err = conn.Write(hello)
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.ErrorIs(t, err, io.EOF)
}

func TestServer_blockAllPolicy(t *testing.T) {
	policy := BlockAllPolicy()

	ctx := t.Context()

	srv := &Server{
		Addr:   "127.0.0.1:0",
		Policy: policy,
		Logger: zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	hello := buildClientHello("github.com")
	_, err = conn.Write(hello)
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.ErrorIs(t, err, io.EOF)
}

func TestServer_ActiveConns(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"tracked.example.com"})
	require.NoError(t, err)

	// Upstream that holds the connection open until signalled.
	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstreamLn.Close() }()

	upstreamDone := make(chan struct{})
	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			return
		}
		<-upstreamDone
		_ = conn.Close()
	}()

	ctx := t.Context()

	srv := &Server{
		Addr:   "127.0.0.1:0",
		Policy: policy,
		Logger: zerolog.Nop(),
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("tcp", upstreamLn.Addr().String())
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	go func() { _ = srv.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int64(0), srv.ActiveConns())

	// Connect with an allowed SNI.
	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)

	hello := buildClientHello("tracked.example.com")
	_, err = conn.Write(hello)
	require.NoError(t, err)

	// Wait for the connection to be forwarded.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(1), srv.ActiveConns())

	// Close the upstream to end forwarding.
	close(upstreamDone)
	_ = conn.Close()
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(0), srv.ActiveConns())
}

func TestServer_interceptedConnectionMITM(t *testing.T) {
	// Verify the proxy routes intercepted domains through the MITM interceptor.
	env := newInterceptTestEnv(t)
	hostname := "intercept.example.com"

	policy, err := NewPolicy(ModeAllow, []string{hostname, "passthrough.example.com"})
	require.NoError(t, err)

	upstreamLn := startUpstreamTLS(t, env, hostname, handleEchoHTTPS)

	li := &LoggingInterceptor{
		Domains:   []string{hostname},
		LeafCache: env.LeafCache,
		Logger:    zerolog.Nop(),
		LogDir:    env.LogDir,
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamLn.Addr().String(), &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}

	ctx := t.Context()

	srv := &Server{
		Addr:        "127.0.0.1:0",
		Policy:      policy,
		Interceptor: li,
		Logger:      zerolog.Nop(),
	}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = proxyLn.Addr().String()
	_ = proxyLn.Close()

	go func() { _ = srv.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Connect via real TLS through the proxy.
	conn, err := tls.Dial("tcp", srv.Addr, &tls.Config{
		ServerName: hostname,
		RootCAs:    env.CAPool,
	})
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Send HTTP request through MITM'd connection.
	reqBody := `{"test":"data"}`
	req, reqErr := http.NewRequest(http.MethodPost, "http://"+hostname+"/v1/messages", strings.NewReader(reqBody))
	require.NoError(t, reqErr)
	req.Header.Set("Connection", "close")
	require.NoError(t, req.Write(conn))

	resp, respErr := http.ReadResponse(bufio.NewReader(conn), req)
	require.NoError(t, respErr)
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, reqBody, string(body))
}

func TestServer_realTLSClientHello(t *testing.T) {
	// Verify the proxy works with a real TLS ClientHello generated by crypto/tls.
	policy, err := NewPolicy(ModeAllow, []string{"real.example.com"})
	require.NoError(t, err)

	// Track that upstream was dialed with correct address.
	dialedAddr := make(chan string, 1)

	// Start a mock upstream that accepts TLS.
	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstreamLn.Close() }()

	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			return
		}
		_ = conn.Close()
	}()

	ctx := t.Context()

	srv := &Server{
		Addr:   "127.0.0.1:0",
		Policy: policy,
		Logger: zerolog.Nop(),
		Dialer: func(_ context.Context, _, address string) (net.Conn, error) {
			dialedAddr <- address
			return net.Dial("tcp", upstreamLn.Addr().String())
		},
	}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = proxyLn.Addr().String()
	_ = proxyLn.Close()

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Use crypto/tls to generate a real ClientHello.
	go func() {
		conn, dialErr := net.DialTimeout("tcp", srv.Addr, time.Second)
		if dialErr != nil {
			return
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         "real.example.com",
			InsecureSkipVerify: true, //nolint:gosec // test only
		})
		_ = tlsConn.SetDeadline(time.Now().Add(2 * time.Second))
		// Handshake will fail since upstream isn't TLS, but we just need the ClientHello sent.
		_ = tlsConn.Handshake()
		_ = tlsConn.Close()
	}()

	select {
	case addr := <-dialedAddr:
		assert.Equal(t, "real.example.com:443", addr)
	case <-time.After(3 * time.Second):
		t.Fatal("dialer was not called")
	}
}

// recordingEmitter captures Emit calls for assertion. Safe for
// concurrent use so it can be shared across the proxy's handler
// goroutines.
type recordingEmitter struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	source, status, host string
}

func (r *recordingEmitter) Emit(source, status, host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{source: source, status: status, host: host})
}

func (r *recordingEmitter) snapshot() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedEvent, len(r.events))
	copy(out, r.events)
	return out
}

// waitForEvent polls the emitter until at least one event matches the
// predicate or the deadline expires. Returns the matched event on
// success and fails the test otherwise.
func waitForEvent(t *testing.T, rec *recordingEmitter, match func(recordedEvent) bool) recordedEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range rec.snapshot() {
			if match(e) {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no matching event observed before deadline")
	return recordedEvent{}
}

func startProxyForTest(t *testing.T, srv *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	go func() { _ = srv.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)
}

func TestServer_emitsForwardOnAllowedHost(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"allowed.example.com"})
	require.NoError(t, err)

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstreamLn.Close() }()
	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
	}()

	rec := &recordingEmitter{}
	srv := &Server{
		Policy:  policy,
		Logger:  zerolog.Nop(),
		Emitter: rec,
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("tcp", upstreamLn.Addr().String())
		},
	}
	startProxyForTest(t, srv)

	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write(buildClientHello("allowed.example.com"))
	require.NoError(t, err)

	evt := waitForEvent(t, rec, func(e recordedEvent) bool {
		return e.source == "proxy" && e.status == "forward"
	})
	assert.Equal(t, "allowed.example.com", evt.host)
}

func TestServer_emitsBlockOnDeniedHost(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"allowed.example.com"})
	require.NoError(t, err)

	rec := &recordingEmitter{}
	srv := &Server{
		Policy:  policy,
		Logger:  zerolog.Nop(),
		Emitter: rec,
	}
	startProxyForTest(t, srv)

	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write(buildClientHello("denied.example.com"))
	require.NoError(t, err)

	evt := waitForEvent(t, rec, func(e recordedEvent) bool {
		return e.source == "proxy" && e.status == "block"
	})
	assert.Equal(t, "denied.example.com", evt.host)
}

func TestServer_emitsNoSniOnEmptySNI(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"*.example.com"})
	require.NoError(t, err)

	rec := &recordingEmitter{}
	srv := &Server{
		Policy:  policy,
		Logger:  zerolog.Nop(),
		Emitter: rec,
	}
	startProxyForTest(t, srv)

	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write(buildClientHello(""))
	require.NoError(t, err)

	evt := waitForEvent(t, rec, func(e recordedEvent) bool {
		return e.source == "fail" && e.status == "no-sni"
	})
	assert.Equal(t, "", evt.host)
}

func TestServer_emitsDialFailOnUpstreamError(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"broken.example.com"})
	require.NoError(t, err)

	rec := &recordingEmitter{}
	srv := &Server{
		Policy:  policy,
		Logger:  zerolog.Nop(),
		Emitter: rec,
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, net.UnknownNetworkError("simulated upstream failure")
		},
	}
	startProxyForTest(t, srv)

	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write(buildClientHello("broken.example.com"))
	require.NoError(t, err)

	evt := waitForEvent(t, rec, func(e recordedEvent) bool {
		return e.source == "fail" && e.status == "dial-fail"
	})
	assert.Equal(t, "broken.example.com", evt.host)
}

func TestServer_nilEmitterIsSafe(t *testing.T) {
	policy, err := NewPolicy(ModeAllow, []string{"allowed.example.com"})
	require.NoError(t, err)

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstreamLn.Close() }()
	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			return
		}
		_ = conn.Close()
	}()

	srv := &Server{
		Policy: policy,
		Logger: zerolog.Nop(),
		// Emitter intentionally nil.
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("tcp", upstreamLn.Addr().String())
		},
	}
	startProxyForTest(t, srv)

	conn, err := net.DialTimeout("tcp", srv.Addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	// Should not panic.
	_, err = conn.Write(buildClientHello("allowed.example.com"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
}

func TestServer_emitsInterceptOnInterceptDomain(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "intercept.example.com"

	policy, err := NewPolicy(ModeAllow, []string{hostname})
	require.NoError(t, err)

	upstreamLn := startUpstreamTLS(t, env, hostname, handleEchoHTTPS)

	li := &LoggingInterceptor{
		Domains:   []string{hostname},
		LeafCache: env.LeafCache,
		Logger:    zerolog.Nop(),
		LogDir:    env.LogDir,
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamLn.Addr().String(), &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}

	rec := &recordingEmitter{}
	srv := &Server{
		Policy:      policy,
		Interceptor: li,
		Logger:      zerolog.Nop(),
		Emitter:     rec,
	}
	startProxyForTest(t, srv)

	// Start a goroutine to open a TLS connection; we don't need the
	// full handshake to complete, only the ClientHello to be consumed
	// so the intercept branch runs.
	go func() {
		conn, dialErr := tls.Dial("tcp", srv.Addr, &tls.Config{
			ServerName: hostname,
			RootCAs:    env.CAPool,
		})
		if dialErr != nil {
			return
		}
		_ = conn.Close()
	}()

	evt := waitForEvent(t, rec, func(e recordedEvent) bool {
		return e.source == "proxy" && e.status == "intercept"
	})
	assert.Equal(t, hostname, evt.host)
}
