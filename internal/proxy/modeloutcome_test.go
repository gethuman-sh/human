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
		name    string
		status  int
		err     error
		errType string
		want    string
	}{
		{"transport error before response", 0, dialErr, "", ClassNetwork},
		{"transport error trumps a status", 200, dialErr, "", ClassNetwork},
		{"200 ok", 200, nil, "", ClassOK},
		{"299 still ok", 299, nil, "", ClassOK},
		{"401 auth", 401, nil, "", ClassAuth},
		{"403 auth", 403, nil, "", ClassAuth},
		{"429 rate limit", 429, nil, "", ClassRateLimit},
		{"529 overload", 529, nil, "", ClassOverload},
		{"400 with no error type is other", 400, nil, "", ClassOther},
		{"400 invalid_request_error stays other", 400, nil, "invalid_request_error", ClassOther},
		{"400 billing_error is spend-limit", 400, nil, "billing_error", ClassSpendLimit},
		{"400 credit-named type is spend-limit", 400, nil, "credit_exhausted", ClassSpendLimit},
		{"error type only matters for a 400", 401, nil, "billing_error", ClassAuth},
		{"500 other", 500, nil, "", ClassOther},
		{"302 other", 302, nil, "", ClassOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(tc.status, tc.err, tc.errType))
		})
	}
}

// The 400 body is peeked ONLY for its fixed error.type enum token: a spend-limit
// envelope classifies as spend-limit, a generic 400 stays other, and no prose is
// ever read (SC-2555 step 2 decision).
func TestEmitOutcome_SpendLimitFrom400Body(t *testing.T) {
	var got ModelCallOutcome
	li := &LoggingInterceptor{RecordOutcome: func(o ModelCallOutcome) { got = o }}

	billing := []byte(`{"type":"error","error":{"type":"billing_error","message":"secret prose that must never be read"}}`)
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 400, nil, time.Now(), billing)
	assert.Equal(t, ClassSpendLimit, got.Class, "a billing_error 400 is a spend-limit")

	generic := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`)
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 400, nil, time.Now(), generic)
	assert.Equal(t, ClassOther, got.Class, "a non-billing 400 stays other")

	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 400, nil, time.Now(), nil)
	assert.Equal(t, ClassOther, got.Class, "a 400 with no body is other, never a panic")
}

// anthropicErrorType reads the enum token and nothing else — a decode failure or
// a body without the field yields "", so a peek can never mistake prose for a
// classification.
func TestAnthropicErrorType(t *testing.T) {
	assert.Equal(t, "billing_error", anthropicErrorType([]byte(`{"error":{"type":"billing_error"}}`)))
	assert.Equal(t, "", anthropicErrorType(nil))
	assert.Equal(t, "", anthropicErrorType([]byte(`not json`)))
	assert.Equal(t, "", anthropicErrorType([]byte(`{"error":{"message":"only prose here"}}`)))
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
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now(), nil)
}

func TestEmitOutcome_GatedOnModelHost(t *testing.T) {
	var got []ModelCallOutcome
	li := &LoggingInterceptor{RecordOutcome: func(o ModelCallOutcome) { got = append(got, o) }}

	li.emitOutcome("1.2.3.4:5", "example.com", 200, nil, time.Now(), nil)
	assert.Empty(t, got, "a non-model host is never accounted")

	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now(), nil)
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
	li.emitOutcome("10.0.0.7:44003", "api.anthropic.com", 200, nil, time.Now(), nil)
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
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 401, nil, time.Now(), nil)
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
		li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now(), nil)
	})
}

// streamingUsageBody is a realistic Anthropic Messages API SSE stream: the
// message_start event carries the final input/cache-create/cache-read counts and
// an initial output_tokens near 1, and the final message_delta carries the
// CUMULATIVE output_tokens at the top level. Intermediate content events carry no
// usage. This is the shape usageFromResponse's first-positive/max strategy exists
// for (SC-2847 AD1).
const streamingUsageBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":100,"cache_creation_input_tokens":50,"cache_read_input_tokens":900,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"secret answer prose that must never be read"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":200}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func TestUsageFromResponse_streaming(t *testing.T) {
	model, in, out, cc, cr := usageFromResponse([]byte(streamingUsageBody))
	assert.Equal(t, "claude-opus-4-8", model)
	assert.Equal(t, 100, in, "input taken from message_start")
	assert.Equal(t, 200, out, "output taken as the cumulative message_delta max, not the near-1 message_start")
	assert.Equal(t, 50, cc)
	assert.Equal(t, 900, cr)
}

func TestUsageFromResponse_nonStreaming(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`)
	model, in, out, cc, cr := usageFromResponse(body)
	assert.Equal(t, "claude-opus-4-8", model)
	assert.Equal(t, 10, in)
	assert.Equal(t, 20, out)
	assert.Equal(t, 0, cc)
	assert.Equal(t, 0, cr)
}

func TestUsageFromResponse_empty(t *testing.T) {
	model, in, out, cc, cr := usageFromResponse(nil)
	assert.Equal(t, "", model)
	assert.Equal(t, 0, in+out+cc+cr)

	model, in, out, cc, cr = usageFromResponse([]byte("data: [DONE]\n\n"))
	assert.Equal(t, "", model)
	assert.Equal(t, 0, in+out+cc+cr, "a stream with no usage event yields zero, never a partial read")
}

func TestEmitOutcome_recordsTokens(t *testing.T) {
	var got ModelCallOutcome
	li := &LoggingInterceptor{RecordOutcome: func(o ModelCallOutcome) { got = o }}
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 200, nil, time.Now(), []byte(streamingUsageBody))
	assert.Equal(t, "claude-opus-4-8", got.Model)
	assert.Equal(t, 100, got.InputTokens)
	assert.Equal(t, 200, got.OutputTokens)
	assert.Equal(t, 50, got.CacheCreateTokens)
	assert.Equal(t, 900, got.CacheReadTokens)
	assert.Equal(t, ClassOK, got.Class)
}

func TestEmitOutcome_failureNoTokens(t *testing.T) {
	var got ModelCallOutcome
	li := &LoggingInterceptor{RecordOutcome: func(o ModelCallOutcome) { got = o }}
	li.emitOutcome("1.2.3.4:5", "api.anthropic.com", 0, errors.New("dial refused"), time.Now(), nil)
	assert.Equal(t, "", got.Model)
	assert.Equal(t, 0, got.InputTokens+got.OutputTokens+got.CacheCreateTokens+got.CacheReadTokens,
		"a call that never produced a response has no cost")
	assert.Equal(t, ClassNetwork, got.Class)
	assert.GreaterOrEqual(t, got.Duration, time.Duration(0))
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

// TestMITM_InflightBracketsModelRequest proves the outstanding-model-request
// signal (SC-3074): a request to the model host fires exactly one +1 then one
// -1 delta, netting to zero once the response completes, and a non-model host
// never fires the callback at all — accounting stays fixed to the model API
// even though MITM interception can cover a wider domain list.
func TestMITM_InflightBracketsModelRequest(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "api.anthropic.com"
	upstreamLn := startUpstreamTLS(t, env, hostname, handleEchoHTTPS)

	var mu sync.Mutex
	var deltas []int
	li := &LoggingInterceptor{
		Domains:   []string{hostname},
		LeafCache: env.LeafCache,
		Logger:    zerolog.Nop(),
		LogDir:    env.LogDir,
		InflightModelRequests: func(_ string, delta int) {
			mu.Lock()
			deltas = append(deltas, delta)
			mu.Unlock()
		},
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamLn.Addr().String(), &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}
	withLogMode(t, LogModeOff)

	doOneRequest(t, env, li, hostname, `{"model":"test"}`)

	// Give the goroutine driving Intercept a moment to record the final delta
	// after the client write (emitOutcome's own ordering already waits for the
	// same completion in doOneRequest, but the -1 fires just before it).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(deltas)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []int{1, -1}, deltas, "exactly one +1 then one -1 per model-host request")
}

// TestMITM_InflightNeverFiresForNonModelHost proves accounting stays gated on
// the model host, independent of the (potentially wider) intercept domain
// list — widening traffic logging must never widen this signal either.
func TestMITM_InflightNeverFiresForNonModelHost(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "example.com"
	upstreamLn := startUpstreamTLS(t, env, hostname, handleEchoHTTPS)

	var mu sync.Mutex
	var deltas []int
	li := &LoggingInterceptor{
		Domains:   []string{hostname},
		LeafCache: env.LeafCache,
		Logger:    zerolog.Nop(),
		LogDir:    env.LogDir,
		InflightModelRequests: func(_ string, delta int) {
			mu.Lock()
			deltas = append(deltas, delta)
			mu.Unlock()
		},
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamLn.Addr().String(), &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}
	withLogMode(t, LogModeOff)

	doOneRequest(t, env, li, hostname, `{"hello":"world"}`)
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, deltas, "a non-model host must never fire the in-flight callback")
}

// TestIntercept_RecordsStreamedTokens proves a streamed (SSE) 200 model call is
// recorded with the four token counts and model id read off the real streamed
// body end-to-end (SC-2847 AD1), not just via the unit test of usageFromResponse.
func TestIntercept_RecordsStreamedTokens(t *testing.T) {
	env := newInterceptTestEnv(t)
	hostname := "api.anthropic.com"
	upstreamLn := startUpstreamTLS(t, env, hostname, handleSSEResponse)

	rec := &recordingSink{}
	li := &LoggingInterceptor{
		Domains:       []string{hostname},
		LeafCache:     env.LeafCache,
		Logger:        zerolog.Nop(),
		LogDir:        env.LogDir,
		RecordOutcome: rec.record,
		Attribute:     func(string) (string, string, bool) { return "SC-2847", "implementation", true },
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamLn.Addr().String(), &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}
	withLogMode(t, LogModeOff)

	doOneRequest(t, env, li, hostname, `{"model":"claude-opus-4-8","stream":true}`)

	o := rec.wait(t, 1)[0]
	assert.Equal(t, ClassOK, o.Class)
	assert.Equal(t, "claude-opus-4-8", o.Model)
	assert.Equal(t, 100, o.InputTokens)
	assert.Equal(t, 200, o.OutputTokens, "cumulative streamed output, not the near-1 message_start value")
	assert.Equal(t, 50, o.CacheCreateTokens)
	assert.Equal(t, 900, o.CacheReadTokens)
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
