package daemon

import "time"

// BoardAgentProgress is the daemon's own liveness judgement of the agent behind
// a card, carried on the board so the viewer can tell a working agent from a
// hung one. Container presence alone cannot: a hung agent's container is
// perfectly healthy. The judgement is made here, by the one probe the zombie
// sweep and the reconcile pass already reap with (AgentProgress.Stalled), so
// the board never grows a second timeout policy that could disagree with the
// machine's (SC-5328).
type BoardAgentProgress struct {
	// Agent is the board agent the judgement is about — the viewer joins it
	// against the container it found, so a judgement about a reaped run's
	// name never decorates a fresh container under the same name.
	Agent string `json:"agent"`
	// DaemonID is the machine that judged. Progress is daemon-local (hook
	// events and the proxy's in-flight count reach only the daemon that
	// launched the agent), so a viewer applies the judgement only to agents on
	// that machine.
	DaemonID string `json:"daemonId"`
	// Stalled is AgentProgress.Stalled at composition time: silent past the
	// budget its outstanding work grants it, and not waiting on a person.
	Stalled bool `json:"stalled"`
	// IdleSeconds is how long the agent has been silent; BudgetSeconds the
	// budget that silence is judged against (3 minutes with nothing
	// outstanding, 30 with work in flight or the model state unknown).
	IdleSeconds   int `json:"idleSeconds"`
	BudgetSeconds int `json:"budgetSeconds"`
	// Outstanding names what the generous budget is granted for, or "" when
	// nothing is outstanding — so the card can say "waiting on a subagent"
	// rather than only "silent".
	Outstanding string `json:"outstanding,omitempty"`
	// Blocked reports the agent is waiting on a person (a permission prompt):
	// never stalled, and not working either.
	Blocked bool `json:"blocked,omitempty"`
}

// MarkAgentProgress overlays the daemon's progress judgement onto each card
// whose agent it knows. Cards whose agents it has never heard from keep a nil
// judgement, which the viewer renders exactly as before: absent evidence is
// never read as a hang (SC-3853's rule, applied at the board).
//
// Runs on the daemon — the only place the probe exists — after Compose and
// after the composed view is remembered, so a cached board served during an
// outage never replays a judgement that was true minutes ago.
func MarkAgentProgress(cards []BoardViewCard, probe AgentProgressProbe, daemonID string, now time.Time) {
	if probe == nil {
		return
	}
	for i := range cards {
		for _, name := range AgentNamesForCard(cards[i]) {
			p, ok := probe(name)
			if !ok {
				continue
			}
			judged := assessProgress(name, p, daemonID, now)
			cards[i].AgentProgress = &judged
			break
		}
	}
}

// assessProgress reduces one AgentProgress to what the board can act on. It
// never recomputes the rule: Stalled and IdleBudget are AgentProgress's own.
func assessProgress(name string, p AgentProgress, daemonID string, now time.Time) BoardAgentProgress {
	stalled, idle := p.Stalled(now)
	return BoardAgentProgress{
		Agent:         name,
		DaemonID:      daemonID,
		Stalled:       stalled,
		IdleSeconds:   int(idle.Seconds()),
		BudgetSeconds: int(p.IdleBudget().Seconds()),
		Outstanding:   p.outstandingWork(),
		Blocked:       p.Blocked,
	}
}

// outstandingWork names the input that earns the agent its generous budget,
// in the order hasOutstandingWork weighs them. "" means nothing is
// outstanding and the short budget applies.
func (p AgentProgress) outstandingWork() string {
	switch {
	case p.InsideTool:
		return "a tool call"
	case p.Subagents > 0:
		return "a subagent"
	case p.ModelRequest == ModelRequestOpen:
		return "a model request"
	case p.ModelRequest == ModelRequestUnknown:
		return "an unknown model state"
	}
	return ""
}
