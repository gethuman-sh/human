package daemon

// NewInflightMarker resolves a proxy connection's remote address to the agent
// name via ips — the same registry Attribute uses — and folds a +1/-1 delta
// into inflight. A connection whose address maps to nothing has its delta
// HELD by pending rather than dropped: an agent with no registry entry reads
// unknown from the probe below, but a mark simply dropped here would stay lost
// even after the mapping arrives a few seconds later, flipping a genuinely
// open request straight to "none" the moment the repair pass catches up — the
// daemon-restart hole a review of this fix found (SC-3853).
func NewInflightMarker(inflight *InflightModelRequests, ips *AgentIPRegistry, pending *PendingModelRequests) func(remoteAddr string, delta int) {
	return func(remoteAddr string, delta int) {
		if name, ok := ips.AgentFor(remoteAddr); ok {
			inflight.Mark(name, delta)
			return
		}
		pending.Hold(hostOnly(remoteAddr), delta)
	}
}

// NewAgentProgressProbe is THE progress probe: the hook stream's record of an
// agent, folded together with the proxy's own outstanding-model-request state.
// It exists once because three consumers assembled it by hand and one of them
// silently did not (SC-3853). Returns nil when there is no hook store, matching
// the "nil probe means never stalled" contract every consumer already honours.
//
// ips is what makes the model-request answer honest: the in-flight counter is
// keyed by agent name, and a name only exists for a connection the registry can
// resolve. With no mapping, a zero count means "nobody could ask", not "nothing
// is open" — so the probe reports unknown and the agent gets the generous
// budget (SC-3853).
func NewAgentProgressProbe(hookEvents *HookEventStore, inflight *InflightModelRequests, ips *AgentIPRegistry) AgentProgressProbe {
	if hookEvents == nil {
		return nil
	}
	return func(name string) (AgentProgress, bool) {
		p, ok := hookEvents.AgentProgress(name)
		if !ok {
			return p, false
		}
		p.ModelRequest = modelRequestState(inflight, ips, name)
		return p, true
	}
}

// modelRequestState answers only what it can. An agent with no IP mapping, or a
// daemon with no in-flight counter wired, yields unknown rather than a false
// negative indistinguishable from idleness.
func modelRequestState(inflight *InflightModelRequests, ips *AgentIPRegistry, agentName string) ModelRequestState {
	if inflight == nil || !ips.Mapped(agentName) {
		return ModelRequestUnknown
	}
	if inflight.Outstanding(agentName) {
		return ModelRequestOpen
	}
	return ModelRequestNone
}
