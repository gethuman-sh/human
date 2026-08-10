package daemon

import (
	"sync"
	"time"
)

// pendingHoldMaxAge bounds how long an unattributed mark waits for its
// mapping. A connection whose agent never gets mapped (it died before any
// repair pass ever ran) would otherwise hold its mark forever; 30 minutes
// matches WorkingIdleGrace — the longest an unmapped agent is spared anyway,
// so nothing is reaped early by pruning a hold that would no longer matter.
const pendingHoldMaxAge = 30 * time.Minute

// pendingMark is one held delta and when it was last touched.
type pendingMark struct {
	delta    int
	markedAt time.Time
}

// PendingModelRequests holds inflight-request marks the daemon could not
// attribute to an agent name at the moment they arrived — the connection's
// host had no entry in the AgentIPRegistry yet. This is the fix for the
// daemon-restart hole: without it, NewInflightMarker simply drops a request
// opened before its mapping exists, RunAgentIPRepair installs the mapping a
// few seconds to 30 seconds later, and the probe reads ModelRequestNone for a
// request that is still genuinely open (SC-3853).
type PendingModelRequests struct {
	mu     sync.Mutex
	byHost map[string]pendingMark
}

// NewPendingModelRequests creates an empty holding area.
func NewPendingModelRequests() *PendingModelRequests {
	return &PendingModelRequests{byHost: make(map[string]pendingMark)}
}

// Hold records delta against host, unattributed. Nil-safe; a no-op for an
// empty host so wiring stays optional.
func (p *PendingModelRequests) Hold(host string, delta int) {
	if p == nil || host == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	mark := p.byHost[host]
	mark.delta += delta
	mark.markedAt = time.Now()
	p.byHost[host] = mark
}

// Replay applies any hold recorded for host to name's in-flight count under
// the mapping that just resolved it, and clears the hold. Callers apply it on
// the line immediately after AgentIPRegistry.Register with no branch between
// them, so a mark that arrived before the mapping existed is never lost to the
// window between "mapped" and "next asked" (SC-3853). Nil-safe.
func (p *PendingModelRequests) Replay(host, name string, inflight *InflightModelRequests) {
	if p == nil || host == "" || name == "" || inflight == nil {
		return
	}
	p.mu.Lock()
	mark, ok := p.byHost[host]
	if ok {
		delete(p.byHost, host)
	}
	p.mu.Unlock()
	if !ok || mark.delta == 0 {
		return
	}
	inflight.Mark(name, mark.delta)
}

// Prune drops holds older than pendingHoldMaxAge. Run unconditionally on every
// repair tick, before the agent listing, so a failed list never skips it —
// pruning must not depend on the same call that can fail (SC-3853).
func (p *PendingModelRequests) Prune(maxAge time.Duration) {
	if p == nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	p.mu.Lock()
	defer p.mu.Unlock()
	for host, mark := range p.byHost {
		if mark.markedAt.Before(cutoff) {
			delete(p.byHost, host)
		}
	}
}
