package daemon

import (
	"net"
	"sync"
)

// AgentIPRegistry maps a container's bridge IP to the board agent name it runs,
// so a model call arriving at the proxy from that IP can be attributed to the
// ticket and stage that made it. Populated when the daemon launches a board
// agent (the container name is known there, its IP resolved by inspect) and
// cleared when the agent is torn down. The registry is the live half of "the
// connection identifies the container" — the property files cannot provide.
type AgentIPRegistry struct {
	mu   sync.RWMutex
	byIP map[string]string // ip -> agent name (e.g. "board-SC-2555-implementation")
}

// NewAgentIPRegistry creates an empty registry.
func NewAgentIPRegistry() *AgentIPRegistry {
	return &AgentIPRegistry{byIP: make(map[string]string)}
}

// Register records that the container at ip runs the named agent. An empty ip or
// name is ignored so a failed inspect never plants a bogus mapping. Safe on a
// nil registry so wiring can be optional.
func (r *AgentIPRegistry) Register(ip, agentName string) {
	if r == nil || ip == "" || agentName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byIP[ip] = agentName
}

// Unregister drops the mapping for ip. Safe on a nil registry and on an unknown ip.
func (r *AgentIPRegistry) Unregister(ip string) {
	if r == nil || ip == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byIP, ip)
}

// Attribute resolves a connection's remote address to the ticket and stage that
// own it. The port is stripped before lookup, and the agent name is decoded with
// the same parseAgentName the failure watcher uses, so the two attributions can
// never diverge. ok is false when the source IP is unknown or its name does not
// decode; the caller then records the outcome with empty attribution rather than
// dropping it. Satisfies proxy.ConnAttributor.
func (r *AgentIPRegistry) Attribute(remoteAddr string) (ticket, stage string, ok bool) {
	if r == nil {
		return "", "", false
	}
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	r.mu.RLock()
	name, found := r.byIP[host]
	r.mu.RUnlock()
	if !found {
		return "", "", false
	}
	pmKey, st, ok := parseAgentName(name)
	if !ok {
		return "", "", false
	}
	return pmKey, string(st), true
}
