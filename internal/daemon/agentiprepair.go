package daemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// agentIPRepairInterval is how often unmapped running agents are re-inspected.
// Slower than the 5s sweep on purpose: an unmapped agent is spared (its
// model-request state reads unknown), so a late repair costs a slower reap of a
// genuine hang, never a killed live run.
const agentIPRepairInterval = 30 * time.Second

// ContainerIPResolver returns a container's bridge IP, or an error.
type ContainerIPResolver func(ctx context.Context, containerID string) (string, error)

// RunAgentIPRepair keeps AgentIPRegistry true to the running agent set.
//
// The registry had exactly one writer — the launcher, at launch — and was never
// rebuilt, so three silent holes left an agent unmappable for the rest of its
// life: a daemon replaced while agents run, an inspect that failed or returned
// no address, and a warm-container relaunch whose registration was skipped
// because Start refused with ErrAlreadyRunning. Every one of them made the
// liveness question unanswerable, and an unanswerable question used to read as
// "idle" (SC-3853).
//
// It also prunes: mappings for agents that are gone are dropped, so a recycled
// bridge IP cannot attribute a live agent's traffic to a dead one.
//
// A missing mapping is logged once per agent, and the recovery is logged too —
// the previous occurrence of this bug was undiagnosable because neither was.
func RunAgentIPRepair(ctx context.Context, agents RunningAgentLister, resolveIP ContainerIPResolver, reg *AgentIPRegistry, pending *PendingModelRequests, inflight *InflightModelRequests, logger zerolog.Logger) {
	if agents == nil || resolveIP == nil || reg == nil {
		return
	}
	logger.Info().Msg("agent IP repair started")
	warned := make(map[string]bool)

	// One pass before the first tick: a daemon that just replaced another
	// inherits its agents, and every one of them is unmapped right now.
	repairAgentIPs(ctx, agents, resolveIP, reg, pending, inflight, warned, logger)

	ticker := time.NewTicker(agentIPRepairInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			repairAgentIPs(ctx, agents, resolveIP, reg, pending, inflight, warned, logger)
		}
	}
}

// repairAgentIPs is one pass: prune stale pending holds unconditionally, then
// prune what is gone, then map what is unmapped.
func repairAgentIPs(ctx context.Context, agents RunningAgentLister, resolveIP ContainerIPResolver, reg *AgentIPRegistry, pending *PendingModelRequests, inflight *InflightModelRequests, warned map[string]bool, logger zerolog.Logger) {
	// Pruned unconditionally and BEFORE the listing below: a held mark must
	// still age out even on a tick whose agent list fails, and the two must
	// never depend on the same call (SC-3853).
	pending.Prune(pendingHoldMaxAge)

	running, err := agents.RunningAgents()
	if err != nil {
		// Never prune on a failed list: an empty live set would drop every
		// mapping and make every agent unanswerable at once.
		logger.Warn().Err(err).Msg("agent IP repair: failed to list agents")
		return
	}

	live := make(map[string]struct{}, len(running))
	for _, a := range running {
		live[a.Name] = struct{}{}
	}
	reg.Retain(live)
	for name := range warned {
		if _, ok := live[name]; !ok {
			delete(warned, name)
		}
	}

	for _, a := range running {
		if a.ContainerID == "" || reg.Mapped(a.Name) {
			continue
		}
		ip, err := resolveIP(ctx, a.ContainerID)
		if err != nil || ip == "" {
			if !warned[a.Name] {
				warned[a.Name] = true
				logger.Warn().Err(err).Str("agent", a.Name).Str("container", a.ContainerID).
					Msg("agent IP repair: no address for a running agent — its model-request state stays unknown and it gets the generous idle budget")
			}
			continue
		}
		reg.Register(ip, a.Name)
		// Replay immediately after Register, no branch between them: a mark
		// that arrived for this host before the mapping existed must not be
		// lost to the window between "mapped" and "next asked" (SC-3853).
		pending.Replay(ip, a.Name, inflight)
		delete(warned, a.Name)
		logger.Info().Str("agent", a.Name).Str("ip", ip).
			Msg("agent IP repair: mapped a running agent the launch never registered")
	}
}
