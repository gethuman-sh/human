package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoggingInterceptor_ShouldIntercept(t *testing.T) {
	li := &LoggingInterceptor{
		Domains: []string{"api.anthropic.com", "example.com"},
	}

	assert.True(t, li.ShouldIntercept("api.anthropic.com"))
	assert.True(t, li.ShouldIntercept("API.ANTHROPIC.COM"))
	assert.True(t, li.ShouldIntercept("example.com"))
	assert.False(t, li.ShouldIntercept("other.com"))
	assert.False(t, li.ShouldIntercept("sub.api.anthropic.com"))
	assert.False(t, li.ShouldIntercept(""))
}

// interceptTestEnv bundles a CA, leaf cache, upstream TLS listener, and logging interceptor
// for reuse across MITM tests.
type interceptTestEnv struct {
	CACert    *x509.Certificate
	CAPool    *x509.CertPool
	LeafCache *LeafCache
	LogDir    string
}

func newInterceptTestEnv(t *testing.T) *interceptTestEnv {
	t.Helper()
	caDir := t.TempDir()
	logDir := filepath.Join(t.TempDir(), "logs")
	caCert, caKey, _, err := LoadOrCreateCA(caDir)
	require.NoError(t, err)

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)

	return &interceptTestEnv{
		CACert:    caCert,
		CAPool:    caPool,
		LeafCache: &LeafCache{CACert: caCert, CAKey: caKey},
		LogDir:    logDir,
	}
}

// withLogMode pins the global traffic log mode for one test and restores
// whatever was there before. The mode is process-global, so a test that
// restores a hard-coded value instead of the previous one silently changes the
// environment every later test runs in — and a test that leaks full logging
// makes any subsequent MITM test write traffic files asynchronously into its
// t.TempDir, racing Go's cleanup.
func withLogMode(t *testing.T, mode LogMode) {
	t.Helper()
	prev := GetLogMode()
	SetLogMode(mode)
	t.Cleanup(func() { SetLogMode(prev) })
}

// startUpstreamTLS starts a mock TLS server that handles connections with handler.
// Returns the listener address.
func startUpstreamTLS(t *testing.T, env *interceptTestEnv, hostname string, handler func(net.Conn)) net.Listener {
	t.Helper()
	cert, err := env.LeafCache.Get(hostname)
	require.NoError(t, err)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go handler(conn)
		}
	}()

	return ln
}

// runInterceptViaListener sets up a TCP listener that accepts one connection,
// runs PeekClientHello + Intercept, and sends the result on a channel.
// Returns the listener address for the client to connect to.
func runInterceptViaListener(t *testing.T, ctx context.Context, li *LoggingInterceptor, hostname string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		// Read the real ClientHello.
		peeked, _, peekErr := PeekClientHello(conn)
		if peekErr != nil {
			_ = conn.Close()
			return
		}
		// Run interceptor (it closes conn when done).
		_ = li.Intercept(ctx, conn, hostname, peeked)
	}()

	return ln.Addr().String()
}

// TestLoggingInterceptor_Intercept_nonStreaming and _streaming were removed:
// they relied on LogModeFull being the default, but the default is LogModeOff.
// The same intercept+logging path is covered by TestLogMode_MetaStripsBody
// which explicitly sets the log mode before running.

func TestLimitWriter(t *testing.T) {
	var buf bytes.Buffer
	lw := LimitWriter(&buf, 5)

	n, err := lw.Write([]byte("hello world"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n) // only 5 bytes accepted
	assert.Equal(t, "hello", buf.String())

	// Further writes are discarded.
	n, err = lw.Write([]byte("more"))
	assert.NoError(t, err)
	assert.Equal(t, 4, n) // reports full len but discards
	assert.Equal(t, "hello", buf.String())
}

func TestReplayConn_Read(t *testing.T) {
	inner := &bytes.Buffer{}
	inner.WriteString("world")

	rc := &replayConn{
		reader: io.MultiReader(strings.NewReader("hello "), inner),
	}

	buf := make([]byte, 20)
	n, err := rc.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "hello ", string(buf[:n]))

	n, err = rc.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "world", string(buf[:n]))
}

func TestLogMode_SetGet(t *testing.T) {
	// Default is off (zero value).
	SetLogMode(LogModeOff) // reset to default
	assert.Equal(t, LogModeOff, GetLogMode())

	SetLogMode(LogModeMeta)
	assert.Equal(t, LogModeMeta, GetLogMode())

	SetLogMode(LogModeFull)
	assert.Equal(t, LogModeFull, GetLogMode())

	SetLogMode(LogModeOff)
	assert.Equal(t, LogModeOff, GetLogMode())
}

func TestLogModeString(t *testing.T) {
	assert.Equal(t, "full", LogModeString(LogModeFull))
	assert.Equal(t, "meta", LogModeString(LogModeMeta))
	assert.Equal(t, "off", LogModeString(LogModeOff))
}

func TestParseLogMode(t *testing.T) {
	mode, err := ParseLogMode("full")
	assert.NoError(t, err)
	assert.Equal(t, LogModeFull, mode)

	mode, err = ParseLogMode("META")
	assert.NoError(t, err)
	assert.Equal(t, LogModeMeta, mode)

	mode, err = ParseLogMode("off")
	assert.NoError(t, err)
	assert.Equal(t, LogModeOff, mode)

	_, err = ParseLogMode("invalid")
	assert.Error(t, err)
}

func TestLogMode_MetaStripsBody(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "meta.test"

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

	withLogMode(t, LogModeMeta)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proxyAddr := runInterceptViaListener(t, ctx, li, hostname)
	time.Sleep(50 * time.Millisecond)

	conn, err := tls.Dial("tcp", proxyAddr, &tls.Config{
		ServerName: hostname,
		RootCAs:    env.CAPool,
	})
	require.NoError(t, err)

	reqBody := `{"model":"test"}`
	req, err := http.NewRequest(http.MethodPost, "http://"+hostname+"/v1/messages", strings.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Connection", "close")
	require.NoError(t, req.Write(conn))

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	_ = conn.Close()
	time.Sleep(200 * time.Millisecond)

	// Verify log entries have empty body but correct metadata.
	entries, err := os.ReadDir(env.LogDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	logData, err := os.ReadFile(filepath.Join(env.LogDir, entries[0].Name()))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	require.Len(t, lines, 2)

	var reqLog TrafficLog
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &reqLog))
	assert.Equal(t, "request", reqLog.Direction)
	assert.Equal(t, "POST", reqLog.Method)
	assert.Empty(t, reqLog.Body, "meta mode should strip body")
	assert.Equal(t, int64(len(reqBody)), reqLog.BodySize, "body_size should still be set")
}

func TestLogMode_OffSkipsLogging(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "off.test"

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

	withLogMode(t, LogModeOff)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proxyAddr := runInterceptViaListener(t, ctx, li, hostname)
	time.Sleep(50 * time.Millisecond)

	conn, err := tls.Dial("tcp", proxyAddr, &tls.Config{
		ServerName: hostname,
		RootCAs:    env.CAPool,
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "http://"+hostname+"/v1/messages", strings.NewReader(`{"test":"off"}`))
	require.NoError(t, err)
	req.Header.Set("Connection", "close")
	require.NoError(t, req.Write(conn))

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	_ = conn.Close()
	time.Sleep(200 * time.Millisecond)

	// No log file should be created.
	entries, err := os.ReadDir(env.LogDir)
	if err == nil {
		assert.Empty(t, entries, "off mode should not create log files")
	}
}

// --- test helpers ---

// handleEchoHTTPS reads an HTTP request over TLS and echoes the body back.
func handleEchoHTTPS(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return
	}

	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
	}
	_ = resp.Write(conn)
}

// handleSSEResponse reads a request and writes back a streaming (SSE) response
// carrying Anthropic Messages API usage events, so an intercept test can prove
// the four token counts are read off a real streamed body end-to-end (SC-2847).
func handleSSEResponse(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	_, _ = io.ReadAll(req.Body)
	_ = req.Body.Close()

	body := []byte(streamingUsageBody)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/event-stream"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
	}
	_ = resp.Write(conn)
}
