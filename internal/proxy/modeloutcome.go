package proxy

import (
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
	ClassOK        = "ok"         // 2xx
	ClassAuth      = "auth"       // 401/403 — an authentication lapse
	ClassRateLimit = "rate-limit" // 429 — throttled
	ClassOverload  = "overload"   // 529 — Anthropic upstream overloaded
	ClassNetwork   = "network"    // a connection that never completed / errored before a response
	ClassOther     = "other"      // any other status (incl. 400: a spend-limit is only distinguishable from the body, out of scope by constraint)
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
}

// classify maps a call to its outcome class from the status line or transport
// error only. A non-nil transportErr means the call never produced a response
// line, so it is a network outcome regardless of any (zero) status code.
func classify(statusCode int, transportErr error) string {
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
	}
	if statusCode >= 200 && statusCode < 300 {
		return ClassOK
	}
	return ClassOther
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

// emitOutcome builds and records a content-free outcome for one model call.
// It is the single accounting seam every MITM path funnels through and it holds
// the load-bearing invariants: it is gated on the model-API host (constraint:
// only the model API is inspected), it is a no-op without a recorder, and its
// whole body — attribution and the record hand-off — is recover()-guarded so a
// recording fault can never break the call it is measuring.
func (li *LoggingInterceptor) emitOutcome(remoteAddr, host string, statusCode int, transportErr error, start time.Time) {
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
	li.RecordOutcome(ModelCallOutcome{
		Ticket:     ticket,
		Stage:      stage,
		Host:       host,
		StatusCode: statusCode,
		Class:      classify(statusCode, transportErr),
		StartedAt:  start,
		Duration:   time.Since(start),
	})
}
