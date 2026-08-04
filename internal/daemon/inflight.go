package daemon

import "sync"

// InflightModelRequests counts, per agent, how many model requests the proxy
// has seen sent but not yet answered. This is the "outstanding model
// request" signal (SC-3074): a request already open is a positive sign of
// life the daemon holds directly from its own proxy, unlike watching for
// output that a thinking phase never produces.
type InflightModelRequests struct {
	mu    sync.Mutex
	byAgt map[string]int
}

// NewInflightModelRequests creates an empty counter.
func NewInflightModelRequests() *InflightModelRequests {
	return &InflightModelRequests{byAgt: make(map[string]int)}
}

// Mark applies delta (+1 on request sent, -1 on response complete/failed) to
// agentName's outstanding count. The count clamps at zero — a stray extra
// decrement (e.g. a response read failing after a delta the read never saw)
// must never go negative and be misread as "very outstanding" — and a count
// that reaches zero drops its key so Outstanding on an unknown agent and
// Outstanding on a settled-to-zero agent behave identically. Safe on a nil
// receiver and a no-op for an empty agentName, so wiring stays optional.
func (m *InflightModelRequests) Mark(agentName string, delta int) {
	if m == nil || agentName == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.byAgt[agentName] + delta
	if n <= 0 {
		delete(m.byAgt, agentName)
		return
	}
	m.byAgt[agentName] = n
}

// Outstanding reports whether agentName currently has a model request in
// flight. False for an unknown agent (a daemon restart clears the map; that
// only ever makes a run look less busy, never masks a real hang) and nil-safe.
func (m *InflightModelRequests) Outstanding(agentName string) bool {
	if m == nil || agentName == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byAgt[agentName] > 0
}
