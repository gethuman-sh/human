package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

const maxBodyLog = 10 * 1024 * 1024 // 10 MB max body logged

// LogMode controls the verbosity of traffic logging.
type LogMode int32

const (
	LogModeOff  LogMode = 0 // no logging (default, zero value)
	LogModeMeta LogMode = 1 // log method, path, status, body_size only
	LogModeFull LogMode = 2 // log everything including body
)

var globalLogMode atomic.Int32

// SetLogMode sets the global traffic log mode.
func SetLogMode(mode LogMode) { globalLogMode.Store(int32(mode)) }

// GetLogMode returns the current global traffic log mode.
func GetLogMode() LogMode { return LogMode(globalLogMode.Load()) }

// LogModeString returns the string representation of a LogMode.
// An unknown value is reported as "off" rather than a more permissive
// mode so a corrupted value can never silently escalate logging.
func LogModeString(m LogMode) string {
	switch m {
	case LogModeFull:
		return "full"
	case LogModeMeta:
		return "meta"
	case LogModeOff:
		return "off"
	default:
		return "off"
	}
}

// ParseLogMode parses a string into a LogMode. Unknown input returns
// LogModeOff along with the parse error, so a typo in config cannot
// accidentally widen what gets captured.
func ParseLogMode(s string) (LogMode, error) {
	switch strings.ToLower(s) {
	case "full":
		return LogModeFull, nil
	case "meta":
		return LogModeMeta, nil
	case "off":
		return LogModeOff, nil
	default:
		return LogModeOff, fmt.Errorf("unknown log mode %q (use off, meta, or full)", s)
	}
}

// Interceptor can intercept and inspect decrypted traffic for specific domains.
type Interceptor interface {
	// ShouldIntercept returns true if this domain should be MITM'd.
	ShouldIntercept(hostname string) bool
	// Intercept handles a MITM'd connection. The peeked bytes contain the
	// already-read ClientHello that must be replayed into the TLS handshake.
	Intercept(ctx context.Context, conn net.Conn, hostname string, peeked []byte) error
}

// TrafficLog is a single JSON-lines entry written to the traffic log.
type TrafficLog struct {
	Timestamp time.Time `json:"ts"`
	Direction string    `json:"dir"` // "request" or "response"
	Host      string    `json:"host"`
	Method    string    `json:"method,omitempty"` // request only
	Path      string    `json:"path,omitempty"`   // request only
	Status    int       `json:"status,omitempty"` // response only
	Body      string    `json:"body"`
	BodySize  int64     `json:"body_size"`
}

// LoggingInterceptor performs MITM interception for configured domains,
// logging HTTP request/response bodies to JSON-lines files.
type LoggingInterceptor struct {
	Domains   []string // exact domain matches to intercept
	LeafCache *LeafCache
	Logger    zerolog.Logger
	LogDir    string // directory for traffic log files

	// logMu serialises appends to the shared daily JSONL file. Each MITM'd
	// connection logs from its own goroutine, and entries can reach ~10 MB
	// (maxBodyLog); O_APPEND only guarantees atomicity up to PIPE_BUF, so
	// without this lock concurrent multi-MB writes can interleave and corrupt
	// the file.
	logMu sync.Mutex

	// Dialer connects to upstream servers. Injected for testing.
	// If nil, tls.Dial is used.
	Dialer func(ctx context.Context, network, address string) (net.Conn, error)
}

// ShouldIntercept returns true if hostname matches a configured intercept domain.
func (li *LoggingInterceptor) ShouldIntercept(hostname string) bool {
	hostname = strings.ToLower(hostname)
	for _, d := range li.Domains {
		if strings.ToLower(d) == hostname {
			return true
		}
	}
	return false
}

// Intercept performs a MITM TLS handshake with the client, dials the real upstream,
// and proxies HTTP traffic while logging request/response bodies.
func (li *LoggingInterceptor) Intercept(ctx context.Context, conn net.Conn, hostname string, peeked []byte) error {
	// Get or generate a leaf cert for this hostname.
	leaf, err := li.LeafCache.Get(hostname)
	if err != nil {
		return errors.WrapWithDetails(err, "generating leaf cert", "hostname", hostname)
	}

	// Wrap conn to replay the already-consumed ClientHello bytes.
	rc := &replayConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(peeked), conn),
	}

	// TLS handshake with the client using the generated leaf cert.
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	}
	clientTLS := tls.Server(rc, tlsConfig)
	if err := clientTLS.HandshakeContext(ctx); err != nil {
		return errors.WrapWithDetails(err, "client TLS handshake failed", "hostname", hostname)
	}
	defer func() { _ = clientTLS.Close() }()

	// Dial real upstream with TLS.
	upstreamConn, err := li.dialUpstream(ctx, hostname)
	if err != nil {
		return errors.WrapWithDetails(err, "upstream dial failed", "hostname", hostname)
	}
	defer func() { _ = upstreamConn.Close() }()

	// Proxy HTTP requests over the established TLS connections.
	return li.proxyHTTP(ctx, clientTLS, upstreamConn, hostname)
}

// proxyHTTP reads HTTP requests from client, forwards to upstream, and logs bodies.
func (li *LoggingInterceptor) proxyHTTP(ctx context.Context, client, upstream net.Conn, hostname string) error {
	clientReader := bufio.NewReader(client)
	upstreamReader := bufio.NewReader(upstream)

	for {
		// Check context before reading next request.
		if ctx.Err() != nil {
			return nil
		}

		// Read HTTP request from client.
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err == io.EOF || isConnClosed(err) {
				return nil // client closed connection
			}
			return errors.WrapWithDetails(err, "reading client request", "hostname", hostname)
		}

		// Read and log request body.
		var reqBody []byte
		if req.Body != nil {
			reqBody, err = io.ReadAll(io.LimitReader(req.Body, maxBodyLog))
			_ = req.Body.Close()
			if err != nil {
				return errors.WrapWithDetails(err, "reading request body", "hostname", hostname)
			}
		}

		li.logTraffic(TrafficLog{
			Timestamp: time.Now(),
			Direction: "request",
			Host:      hostname,
			Method:    req.Method,
			Path:      req.URL.RequestURI(),
			Body:      string(reqBody),
			BodySize:  int64(len(reqBody)),
		})

		// Forward request to upstream with body.
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		if err := req.Write(upstream); err != nil {
			return errors.WrapWithDetails(err, "writing to upstream", "hostname", hostname)
		}

		// Read response from upstream.
		resp, err := http.ReadResponse(upstreamReader, req)
		if err != nil {
			return errors.WrapWithDetails(err, "reading upstream response", "hostname", hostname)
		}

		// Capture response body while writing to client.
		var respBodyBuf bytes.Buffer
		resp.Body = io.NopCloser(io.TeeReader(resp.Body, LimitWriter(&respBodyBuf, maxBodyLog)))

		// Write response to client (this streams SSE events through).
		writeErr := resp.Write(client)
		_ = resp.Body.Close()

		// Persist whatever we captured before the write failure so
		// logs retain the partial response for debugging; otherwise a
		// single broken streaming write throws away the entire body.
		li.logTraffic(TrafficLog{
			Timestamp: time.Now(),
			Direction: "response",
			Host:      hostname,
			Path:      req.URL.RequestURI(),
			Status:    resp.StatusCode,
			Body:      respBodyBuf.String(),
			BodySize:  int64(respBodyBuf.Len()),
		})

		if writeErr != nil {
			return errors.WrapWithDetails(writeErr, "writing to client", "hostname", hostname)
		}

		// If connection is not keep-alive, stop.
		if resp.Close || req.Close {
			return nil
		}
	}
}

// logTraffic appends a TrafficLog entry to the daily JSON-lines file.
// Respects the global log mode: off skips entirely, meta strips the body.
func (li *LoggingInterceptor) logTraffic(entry TrafficLog) {
	mode := GetLogMode()
	if mode == LogModeOff {
		return
	}
	if mode == LogModeMeta {
		entry.Body = ""
	}

	if li.LogDir == "" {
		return
	}

	if err := os.MkdirAll(li.LogDir, 0o700); err != nil {
		li.Logger.Warn().Err(err).Msg("failed to create traffic log dir")
		return
	}

	filename := time.Now().Format("2006-01-02") + ".jsonl"
	path := filepath.Join(li.LogDir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path built from LogDir
	if err != nil {
		li.Logger.Warn().Err(err).Str("path", path).Msg("failed to open traffic log")
		return
	}
	defer func() { _ = f.Close() }()

	data, err := json.Marshal(entry)
	if err != nil {
		li.Logger.Warn().Err(err).Msg("failed to marshal traffic log entry")
		return
	}
	data = append(data, '\n')

	// Serialise the append so concurrent connections cannot interleave their
	// (potentially multi-MB) lines and corrupt the JSONL.
	li.logMu.Lock()
	defer li.logMu.Unlock()
	if _, err := f.Write(data); err != nil {
		li.Logger.Warn().Err(err).Msg("failed to write traffic log entry")
	}
}

func (li *LoggingInterceptor) dialUpstream(ctx context.Context, hostname string) (net.Conn, error) {
	if li.Dialer != nil {
		return li.Dialer(ctx, "tcp", net.JoinHostPort(hostname, "443"))
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 30 * time.Second},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: hostname,
		},
	}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(hostname, "443"))
}

// replayConn wraps a net.Conn, replaying peeked bytes before reading from
// the underlying connection.
type replayConn struct {
	net.Conn
	reader io.Reader
}

func (c *replayConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

// limitWriter wraps an io.Writer and stops writing after n bytes.
type limitWriter struct {
	w io.Writer
	n int64
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.n <= 0 {
		return len(p), nil // discard but report success
	}
	if int64(len(p)) > lw.n {
		p = p[:lw.n]
	}
	n, err := lw.w.Write(p)
	lw.n -= int64(n)
	return n, err
}

// LimitWriter returns a writer that writes at most n bytes to w.
func LimitWriter(w io.Writer, n int64) io.Writer {
	return &limitWriter{w: w, n: n}
}

// isConnClosed checks if an error indicates a closed connection.
func isConnClosed(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe")
}
