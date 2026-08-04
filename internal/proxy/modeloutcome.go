package proxy

import (
	"encoding/json"
	"strings"
	"time"
)

// DefaultModelAPIHost is the model-API host whose call outcomes are accounted.
// Recording is gated on this host specifically, independent of the intercept
// domain list, so widening traffic logging later can never widen accounting
// (SC-2555 constraint: only the model API is inspected).
const DefaultModelAPIHost = "api.anthropic.com"

// Model-call outcome classes. These are derived from the HTTP status line or
// the transport error ALONE — never from response content — so classification
// costs nothing beyond what the MITM loop already holds and stores no transcript.
const (
	ClassOK         = "ok"          // 2xx
	ClassAuth       = "auth"        // 401/403 — an authentication lapse
	ClassRateLimit  = "rate-limit"  // 429 — throttled
	ClassOverload   = "overload"    // 529 — Anthropic upstream overloaded
	ClassSpendLimit = "spend-limit" // 400 whose error.type enum token marks a billing/credit exhaustion
	ClassNetwork    = "network"     // a connection that never completed / errored before a response
	ClassOther      = "other"       // any other status (incl. a plain 400)
)

// ModelCallOutcome is a content-free record of a single model-API call. It
// carries counts, status, class and timing only — never any prompt or response
// body — because the point is accounting, not a transcript (SC-2555 constraint).
type ModelCallOutcome struct {
	// Ticket and Stage attribute the call to the work that made it, resolved
	// from the connection source. Empty when the source could not be attributed
	// (an unattributed model failure is still worth recording).
	Ticket string `json:"ticket"`
	Stage  string `json:"stage"`
	Host   string `json:"host"`
	// StatusCode is 0 when the call failed before any response line existed
	// (a refused/never-completed connection).
	StatusCode int           `json:"status_code"`
	Class      string        `json:"class"`
	StartedAt  time.Time     `json:"started_at"`
	Duration   time.Duration `json:"duration"`
	// Model is the raw model id read from the response. Empty when the body
	// carried no usage (a failure, or a bodiless response). The four token
	// counts are the fixed metadata usage fields — never prompt or completion
	// prose — so the record stays content-free (SC-2555); they price per-ticket
	// cost at read time via claude.CostUSD (SC-2847).
	Model             string `json:"model,omitempty"`
	InputTokens       int    `json:"input_tokens,omitempty"`
	OutputTokens      int    `json:"output_tokens,omitempty"`
	CacheCreateTokens int    `json:"cache_create_tokens,omitempty"`
	CacheReadTokens   int    `json:"cache_read_tokens,omitempty"`
}

// classify maps a call to its outcome class from the status line, the transport
// error, and — for a 400 alone — the response envelope's fixed error.type enum
// token. A non-nil transportErr means the call never produced a response line,
// so it is a network outcome regardless of any (zero) status code. errType is
// empty for every path except a 400 with a decodable envelope; it is the ONLY
// signal that splits a billing/credit exhaustion from an ordinary bad request,
// and it is a machine classification code, never prose (SC-2555).
func classify(statusCode int, transportErr error, errType string) string {
	if transportErr != nil {
		return ClassNetwork
	}
	switch statusCode {
	case 401, 403:
		return ClassAuth
	case 429:
		return ClassRateLimit
	case 529:
		return ClassOverload
	case 400:
		if isSpendLimitType(errType) {
			return ClassSpendLimit
		}
		return ClassOther
	}
	if statusCode >= 200 && statusCode < 300 {
		return ClassOK
	}
	return ClassOther
}

// anthropicErrorType extracts ONLY the fixed error.type enum token from a
// model-API error envelope. It decodes a struct that carries that single field
// and nothing else, so no prompt/response prose is ever read and the body is
// not retained — the token is a machine classification code, not content
// (SC-2555: a fixed enum token is metadata, the only signal that splits a
// spend-limit 400 from a generic one). It returns "" for any body that does not
// carry the token or does not parse.
func anthropicErrorType(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Type
}

// isSpendLimitType reports whether a model-API error.type enum token denotes a
// billing/credit exhaustion. The match is on the enum token alone (Anthropic's
// billing_error and any billing/credit-named variant), never on the human
// message that accompanies it, keeping the classification content-free.
func isSpendLimitType(errType string) bool {
	t := strings.ToLower(strings.TrimSpace(errType))
	return strings.Contains(t, "billing") || strings.Contains(t, "credit")
}

// tokenUsage is the fixed token-count block shared by the streaming and
// non-streaming Messages API envelopes. Only these named integer fields are
// read — never any prompt or completion prose — so the outcome stays
// content-free (SC-2555). The field names match Claude Code's local JSONL
// envelope (internal/claude/usage.go), which shares the same inner shape.
type tokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// usageEnvelope decodes the model id and usage block from both wire shapes: the
// non-streaming top-level object (Model/Usage at the root) and the streaming
// events (message_start nests message.model + message.usage; message_delta puts
// the cumulative usage at the root).
type usageEnvelope struct {
	Model   string `json:"model"` // present on a non-streaming top-level object
	Message struct {
		Model string      `json:"model"`
		Usage *tokenUsage `json:"usage"`
	} `json:"message"`
	Usage *tokenUsage `json:"usage"` // top-level: non-streaming object AND message_delta
}

// usageFromResponse extracts the four token counts and the model id from a
// model-API 2xx body. It reads ONLY fixed token-count fields and the model id —
// no content is ever inspected or retained (SC-2555).
//
// Anthropic streams the Messages API as SSE `data: {json}` lines: message_start
// carries message.model plus the final input/cache-create/cache-read counts (and
// an initial output near 1), and the final message_delta carries the CUMULATIVE
// output_tokens at the top level. So input/cache-create/cache-read are taken
// last-positive (they appear once, in message_start) and output is taken as the
// running max (message_delta supersedes the near-zero message_start value) —
// which neither misses nor double-counts across the events. A non-streaming call
// returns one JSON object with a top-level usage + model, handled by the
// whole-body fallback when no `data:` line parsed.
func usageFromResponse(body []byte) (model string, in, out, cacheCreate, cacheRead int) {
	if len(body) == 0 {
		return "", 0, 0, 0, 0
	}
	var acc usageAcc
	matched := false
	for _, line := range strings.Split(string(body), "\n") {
		payload, ok := sseDataPayload(line)
		if !ok {
			continue
		}
		var env usageEnvelope
		if json.Unmarshal([]byte(payload), &env) == nil {
			matched = true
			acc.apply(env)
		}
	}
	if !matched {
		var env usageEnvelope
		if json.Unmarshal(body, &env) == nil {
			acc.apply(env)
		}
	}
	return acc.model, acc.in, acc.out, acc.cacheCreate, acc.cacheRead
}

// sseDataPayload returns the JSON payload of an SSE `data:` line, or ok=false for
// any other line (blank, `event:`, a bare `data: [DONE]` terminator).
func sseDataPayload(line string) (payload string, ok bool) {
	line = strings.TrimSpace(line) // also strips a trailing \r if the transport used CRLF
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return "", false
	}
	return payload, true
}

// usageAcc folds one or more usage envelopes into the four token counts and the
// model id, taking input/cache last-positive (they appear once, in message_start)
// and output as the running max (message_delta supersedes the near-1 start value).
type usageAcc struct {
	model                           string
	in, out, cacheCreate, cacheRead int
}

func (a *usageAcc) apply(env usageEnvelope) {
	if env.Message.Model != "" {
		a.model = env.Message.Model
	} else if env.Model != "" {
		a.model = env.Model
	}
	a.merge(env.Message.Usage)
	a.merge(env.Usage)
}

func (a *usageAcc) merge(u *tokenUsage) {
	if u == nil {
		return
	}
	if u.InputTokens > 0 {
		a.in = u.InputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		a.cacheCreate = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		a.cacheRead = u.CacheReadInputTokens
	}
	if u.OutputTokens > a.out {
		a.out = u.OutputTokens
	}
}

// ModelOutcomeRecorder receives a content-free outcome for accounting. It is
// injected into the interceptor as a function field so the proxy package never
// depends on the daemon's sink; a nil recorder disables recording entirely.
type ModelOutcomeRecorder func(ModelCallOutcome)

// ConnAttributor maps a client connection's remote address to the ticket and
// stage that own it. ok is false when the source is unknown; the caller then
// records the outcome with empty attribution rather than dropping it.
type ConnAttributor func(remoteAddr string) (ticket, stage string, ok bool)

// modelAPIHost returns the host outcome recording is gated on: the configured
// override, or the default model-API host when unset.
func (li *LoggingInterceptor) modelAPIHost() string {
	if h := strings.TrimSpace(li.ModelAPIHost); h != "" {
		return h
	}
	return DefaultModelAPIHost
}

// isModelHost reports whether host is the model API whose calls are accounted.
func (li *LoggingInterceptor) isModelHost(host string) bool {
	return strings.EqualFold(strings.TrimSpace(host), li.modelAPIHost())
}

// markInflight reports one +1/-1 delta for an in-flight model request, gated
// on the model host and nil-safe like emitOutcome — a fault in accounting
// must never break the call it is measuring, and only the traffic that
// matters (the model API) ever feeds the signal (SC-3074).
func (li *LoggingInterceptor) markInflight(remoteAddr, host string, delta int) {
	if li.InflightModelRequests == nil || !li.isModelHost(host) {
		return
	}
	defer func() { _ = recover() }()
	li.InflightModelRequests(remoteAddr, delta)
}

// emitOutcome builds and records a content-free outcome for one model call.
// It is the single accounting seam every MITM path funnels through and it holds
// the load-bearing invariants: it is gated on the model-API host (constraint:
// only the model API is inspected), it is a no-op without a recorder, and its
// whole body — attribution and the record hand-off — is recover()-guarded so a
// recording fault can never break the call it is measuring.
// body carries the captured response bytes so a 400 can be split into
// spend-limit vs other by its error.type enum token, and — on the 2xx path — so
// the four token counts and model id can be read for per-ticket cost accounting
// (SC-2847). It is nil on every path that has no response line (a transport
// failure) and is never stored; only fixed metadata fields are read, so the
// outcome struct stays content-free (SC-2555). No other outcome touches the body.
func (li *LoggingInterceptor) emitOutcome(remoteAddr, host string, statusCode int, transportErr error, start time.Time, body []byte) {
	if li.RecordOutcome == nil || !li.isModelHost(host) {
		return
	}
	defer func() { _ = recover() }()

	ticket, stage := "", ""
	if li.Attribute != nil {
		if t, s, ok := li.Attribute(remoteAddr); ok {
			ticket, stage = t, s
		}
	}
	errType := ""
	if statusCode == 400 {
		errType = anthropicErrorType(body)
	}
	var model string
	var inTok, outTok, cacheCreate, cacheRead int
	if statusCode >= 200 && statusCode < 300 {
		model, inTok, outTok, cacheCreate, cacheRead = usageFromResponse(body)
	}
	li.RecordOutcome(ModelCallOutcome{
		Ticket:            ticket,
		Stage:             stage,
		Host:              host,
		Model:             model,
		InputTokens:       inTok,
		OutputTokens:      outTok,
		CacheCreateTokens: cacheCreate,
		CacheReadTokens:   cacheRead,
		StatusCode:        statusCode,
		Class:             classify(statusCode, transportErr, errType),
		StartedAt:         start,
		Duration:          time.Since(start),
	})
}
