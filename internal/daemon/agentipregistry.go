package daemon

import (
	"net"
	"sync"

	"github.com/gethuman-sh/human/internal/agentname"
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

// Mapped reports whether any address resolves to agentName. It is the question
// the liveness probe must ask before reading an in-flight count: a count of
// zero for an agent nobody can name means "nobody could ask", not "nothing is
// open" (SC-3853). Nil-safe; the map is small (one entry per live agent), so
// the scan costs nothing at a 5-second tick.
func (r *AgentIPRegistry) Mapped(agentName string) bool {
	if r == nil || agentName == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.byIP {
		if name == agentName {
			return true
		}
	}
	return false
}

// Retain drops every mapping whose agent is not in live. Teardown happens on
// several paths (a reap, a cleanup, a close-cancel, a person running
// `human agent stop`), and a per-path Unregister is one a new path forgets —
// so the repair pass prunes from the running set instead. Without it a
// recycled Docker bridge IP attributes a new agent's calls, and its in-flight
// marks, to a dead agent's name. Nil-safe; an empty live set is honoured, so
// callers must skip the call when they could not list agents.
func (r *AgentIPRegistry) Retain(live map[string]struct{}) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for ip, name := range r.byIP {
		if _, ok := live[name]; !ok {
			delete(r.byIP, ip)
		}
	}
}

// hostOnly strips the port from a remote address, matching the key every
// method on this registry uses. Connections arrive as ip:port; the registry
// (and PendingModelRequests, which shares its key space) is keyed by the bare
// host, so an address that fails to parse is used as-is rather than dropped —
// callers already treat "not found" as the safe default.
func hostOnly(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
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
	host := hostOnly(remoteAddr)
	r.mu.RLock()
	name, found := r.byIP[host]
	r.mu.RUnlock()
	if !found {
		return "", "", false
	}
	pmKey, st, ok := parseAgentName(name)
	if ok {
		return pmKey, string(st), true
	}
	// A per-ticket auxiliary run (relate, idea-draft) is not a board stage and
	// so has no stage grammar — but it spends real money against a real ticket,
	// and an outcome with no ticket is dropped by the ledger rather than stored
	// unattributed. Its prefix is its stage. Only Attribute learns this grammar:
	// widening parseAgentName would enrol these runs in the reaper's board-only
	// sweeps under a stage token no vocabulary declares.
	if key, prefix, aux := agentname.ParseAux(name); aux {
		return key, prefix, true
	}
	return "", "", false
}

// AgentFor resolves a connection's remote address to the raw agent name — the
// key the in-flight model-request map uses (InflightModelRequests), not the
// decoded ticket+stage Attribute returns. The port is stripped before lookup,
// matching Attribute. ok is false when the source IP is unknown; nil-safe so
// wiring stays optional.
func (r *AgentIPRegistry) AgentFor(remoteAddr string) (agentName string, ok bool) {
	if r == nil {
		return "", false
	}
	host := hostOnly(remoteAddr)
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, found := r.byIP[host]
	if !found {
		return "", false
	}
	return name, true
}
