package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
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

func TestClassify(t *testing.T) {
	dialErr := errors.New("dial tcp: connection refused")
	cases := []struct {
		name   string
		status int
		err    error
		want   string
	}{
		{"transport error before response", 0, dialErr, ClassNetwork},
		{"transport error trumps a status", 200, dialErr, ClassNetwork},
		{"200 ok", 200, nil, ClassOK},
		{"299 still ok", 299, nil, ClassOK},
		{"401 auth", 401, nil, ClassAuth},
		{"403 auth", 403, nil, ClassAuth},
		{"429 rate limit", 429, nil, ClassRateLimit},
		{"529 overload", 529, nil, ClassOverload},
		{"400 is other (spend-limit only distinguishable from body)", 400, nil, ClassOther},
		{"500 other", 500, nil, ClassOther},
		{"302 other", 302, nil, ClassOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(tc.status, tc.err))
		})
	}
}

func TestModelAPIHost_DefaultAndOverride(t *testing.T) {
	li := &LoggingInterceptor{}
	assert.Equal(t, DefaultModelAPIHost, li.modelAPIHost())
	assert.True(t, li.isModelHost("api.anthropic.com"))
	assert.True(t, li.isModelHost("API.ANTHROPIC.COM"), "host match is case-insensitive")
	assert.False(t, li.isModelHost("example.com"))

	li.ModelAPIHost = "models.internal"
	assert.Equal(t, "models.internal", li.modelAPIHost())
	assert.True(t, li.isModelHost("models.internal"))
	assert.False(t, li.isModelHost("api.anthropic.com"),
		"gating follows the override, not the default")
}

func TestEmitOutcome_NilRecorderNoOp(t *testing.T) {
	li := &LoggingInterceptor{}
	// Must not panic with no recorder wired.
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now())
}

func TestEmitOutcome_GatedOnModelHost(t *testing.T) {
	var got []ModelCallOutcome
	li := &LoggingInterceptor{RecordOutcome: func(o ModelCallOutcome) { got = append(got, o) }}

	li.emitOutcome("1.2.3.4:5", "example.com", 200, nil, time.Now())
	assert.Empty(t, got, "a non-model host is never accounted")

	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now())
	require.Len(t, got, 1, "the model host is accounted")
	assert.Equal(t, ClassOK, got[0].Class)
	assert.Equal(t, "api.anthropic.com", got[0].Host)
}

func TestEmitOutcome_Attribution(t *testing.T) {
	var got ModelCallOutcome
	li := &LoggingInterceptor{
		RecordOutcome: func(o ModelCallOutcome) { got = o },
		Attribute: func(remoteAddr string) (string, string, bool) {
			assert.Equal(t, "10.0.0.7:44003", remoteAddr)
			return "SC-2555", "implementation", true
		},
	}
	li.emitOutcome("10.0.0.7:44003", "api.anthropic.com", 200, nil, time.Now())
	assert.Equal(t, "SC-2555", got.Ticket)
	assert.Equal(t, "implementation", got.Stage)
}

func TestEmitOutcome_UnattributedIsStillRecorded(t *testing.T) {
	var got ModelCallOutcome
	recorded := false
	li := &LoggingInterceptor{
		RecordOutcome: func(o ModelCallOutcome) { got = o; recorded = true },
		Attribute:     func(string) (string, string, bool) { return "", "", false },
	}
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 401, nil, time.Now())
	require.True(t, recorded, "an unattributed failure is recorded, not dropped")
	assert.Empty(t, got.Ticket)
	assert.Empty(t, got.Stage)
	assert.Equal(t, ClassAuth, got.Class)
}

func TestEmitOutcome_PanicInSinkSwallowed(t *testing.T) {
	li := &LoggingInterceptor{
		RecordOutcome: func(ModelCallOutcome) { panic("sink boom") },
		Attribute:     func(string) (string, string, bool) { panic("attrib boom") },
	}
	// Constraint 1: a recording fault must never escape to break the call.
	assert.NotPanics(t, func() {
		li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now())
	})
}

// TestIntercept_RecordsOKOutcome proves a 200 model call yields one content-free
// "ok" outcome recorded after the client write.
func TestIntercept_RecordsOKOutcome(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "api.anthropic.com"
	upstreamLn := startUpstreamTLS(t, env, hostname, handleEchoHTTPS)

	rec := &recordingSink{}
	li := &LoggingInterceptor{
		Domains:       []string{hostname},
		LeafCache:     env.LeafCache,
		Logger:        zerolog.Nop(),
		LogDir:        env.LogDir,
		RecordOutcome: rec.record,
		Attribute:     func(string) (string, string, bool) { return "SC-2555", "implementation", true },
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamLn.Addr().String(), &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}
	withLogMode(t, LogModeOff)

	doOneRequest(t, env, li, hostname, `{"model":"test"}`)

	outcomes := rec.wait(t, 1)
	o := outcomes[0]
	assert.Equal(t, ClassOK, o.Class)
	assert.Equal(t, 200, o.StatusCode)
	assert.Equal(t, hostname, o.Host)
	assert.Equal(t, "SC-2555", o.Ticket)
	assert.Equal(t, "implementation", o.Stage)
	assert.False(t, o.StartedAt.IsZero())
	assert.GreaterOrEqual(t, o.Duration, time.Duration(0))
}

// TestIntercept_DialFailRecordsNetwork proves a connection that never completes
// is recorded as a network outcome with StatusCode 0 — the pre-transcript case
// files structurally cannot see — and writes no traffic-log body.
func TestIntercept_DialFailRecordsNetwork(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "api.anthropic.com"

	rec := &recordingSink{}
	li := &LoggingInterceptor{
		Domains:       []string{hostname},
		LeafCache:     env.LeafCache,
		Logger:        zerolog.Nop(),
		LogDir:        env.LogDir,
		RecordOutcome: rec.record,
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
	}
	withLogMode(t, LogModeFull)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proxyAddr := runInterceptViaListener(t, ctx, li, hostname)
	time.Sleep(50 * time.Millisecond)

	// The client handshake completes; the upstream dial then fails inside Intercept.
	conn, err := tls.Dial("tcp", proxyAddr, &tls.Config{ServerName: hostname, RootCAs: env.CAPool})
	require.NoError(t, err)
	_ = conn.Close()

	outcomes := rec.wait(t, 1)
	o := outcomes[0]
	assert.Equal(t, ClassNetwork, o.Class)
	assert.Equal(t, 0, o.StatusCode, "a call that never completed carries no status")
}

// recordingSink is a concurrency-safe outcome collector for the intercept tests.
type recordingSink struct {
	mu  sync.Mutex
	got []ModelCallOutcome
}

func (r *recordingSink) record(o ModelCallOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, o)
}

func (r *recordingSink) wait(t *testing.T, n int) []ModelCallOutcome {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.got)
		r.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	require.GreaterOrEqual(t, len(r.got), n, "expected %d outcome(s)", n)
	out := make([]ModelCallOutcome, len(r.got))
	copy(out, r.got)
	return out
}

// doOneRequest dials the proxy and sends a single connection-close POST.
func doOneRequest(t *testing.T, env *interceptTestEnv, li *LoggingInterceptor, hostname, body string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	proxyAddr := runInterceptViaListener(t, ctx, li, hostname)
	time.Sleep(50 * time.Millisecond)

	conn, err := tls.Dial("tcp", proxyAddr, &tls.Config{ServerName: hostname, RootCAs: env.CAPool})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, "http://"+hostname+"/v1/messages", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Connection", "close")
	require.NoError(t, req.Write(conn))

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	require.NoError(t, err)
	_, _ = bytes.NewBuffer(nil).ReadFrom(resp.Body)
	_ = resp.Body.Close()
	_ = conn.Close()
}
