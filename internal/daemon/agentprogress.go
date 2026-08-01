package daemon

import (
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

// Idle budgets after which an agent that is still running is treated as hung.
//
// They are deliberately two numbers, not one, because "no event for N minutes"
// means something different depending on what the agent was doing. Between tool
// calls a model acts within seconds, so silence is abnormal fast; inside a tool
// call the agent is legitimately blocked on a command that may run for a long
// time (a full test suite), and no event can arrive until it returns.
//
// A single fixed timeout cannot serve both, which is exactly why the wall-clock
// grace it replaces was wrong in both directions at once.
//
// ThinkingIdleGrace bounds silence of EVERY kind between tool calls — no hook
// event AND no transcript output — not merely the absence of a hook event.
// Extended reasoning between tool calls emits no hook event at all (thinking
// produces none) but streams to the agent's transcript the whole time, so
// LastProgressAt is what tells a thinking agent apart from a genuinely hung
// one. This is why the number stays three minutes rather than growing: it was
// never really "no hook event for 3 minutes", it was "no sign of life of any
// kind for 3 minutes" — the number was right, the evidence feeding it was
// incomplete (SC-2447).
var (
	// ThinkingIdleGrace bounds silence between tool calls.
	ThinkingIdleGrace = 3 * time.Minute
	// ToolIdleGrace bounds one tool call. Generous on purpose: killing a
	// running suite is far worse than noticing a hang a few minutes later.
	ToolIdleGrace = 30 * time.Minute
)

// AgentProgress is the last observed sign of life from one agent.
//
// It is progress, not existence. A crashed agent and a hung agent both stop
// emitting hook events, while a container-liveness check reports a hung agent
// as perfectly healthy — which is why liveness alone can never detect a hang.
type AgentProgress struct {
	// LastEventAt is when this agent last did anything observable via a hook
	// event (a tool call starting or finishing, a notification).
	LastEventAt time.Time
	// LastProgressAt is when this agent last produced OUTPUT that is not a
	// hook event — a byte written to its own transcript while reasoning. A
	// model that thinks for minutes between tool calls emits no hook event
	// but keeps streaming to its transcript, so this is what tells thinking
	// apart from a hang (SC-2447). Zero when no transcript output has been
	// observed yet; that is never treated as more recent than LastEventAt.
	LastProgressAt time.Time
	// LastEvent is the hook event name that produced LastEventAt.
	LastEvent string
	// Tool is the tool currently executing, when InsideTool is set.
	Tool string
	// InsideTool reports a PreToolUse with no matching PostToolUse yet: the
	// agent is waiting on a command, not idle.
	InsideTool bool
	// Blocked reports the agent is waiting on a human (a permission prompt).
	// That is neither progress nor a hang — it needs an answer, not a retry.
	Blocked bool
}

// IdleBudget is how long this agent may stay silent before it counts as hung.
func (p AgentProgress) IdleBudget() time.Duration {
	if p.InsideTool {
		return ToolIdleGrace
	}
	return ThinkingIdleGrace
}

// Stalled reports whether the agent has been silent past its budget, and for
// how long. A blocked agent is never stalled: it is waiting for a person, and
// relaunching it would discard the question rather than answer it.
//
// Idle is measured from the more recent of the last hook event and the last
// transcript output: a thinking agent produces the latter continuously with
// none of the former, and treating hook silence alone as the clock is exactly
// what misjudged real work as a hang (SC-2447).
func (p AgentProgress) Stalled(now time.Time) (bool, time.Duration) {
	last := p.LastEventAt
	if p.LastProgressAt.After(last) {
		last = p.LastProgressAt
	}
	idle := now.Sub(last)
	if p.Blocked {
		return false, idle
	}
	return idle > p.IdleBudget(), idle
}

// AgentProgressProbe reports the last progress seen from an agent. The second
// result is false when nothing is known about it — a daemon that restarted, or
// an agent that has yet to emit its first event.
type AgentProgressProbe func(agentName string) (AgentProgress, bool)

// trackProgress folds one hook event into the per-agent progress map.
//
// This is kept as its own map rather than derived from the event ring on
// demand: the ring evicts under load (a 200-event per-session cap) and is empty
// after a restart, so a quiet-but-working agent could have its last event aged
// out and be misread as hung. Losing progress that way kills live work, which is
// the one direction this must never fail in. One entry per agent is cheap and
// cannot be evicted by another agent's traffic.
func trackProgress(progress map[string]AgentProgress, evt hookevents.Event) {
	if evt.AgentName == "" {
		return
	}
	// A finished agent is not a stalled one; drop it so a completed run cannot
	// later be mistaken for a hang.
	if evt.EventName == "Stop" || evt.EventName == "SessionEnd" || evt.EventName == "StopFailure" {
		delete(progress, evt.AgentName)
		return
	}

	at := evt.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	p := progress[evt.AgentName]
	p.LastEventAt = at
	p.LastEvent = evt.EventName

	switch evt.EventName {
	case "PreToolUse":
		p.InsideTool = true
		p.Tool = evt.ToolName
		p.Blocked = false
	case "PostToolUse":
		p.InsideTool = false
		p.Tool = ""
		p.Blocked = false
	case "Notification":
		// Claude is asking for permission: the agent is waiting on a human.
		p.Blocked = true
	default:
		p.Blocked = false
	}
	progress[evt.AgentName] = p
}

// recordAgentOutput bumps an existing agent's LastProgressAt to at — the
// reasoning heartbeat the zombie sweep folds in from the container's
// transcript mtime so a thinking agent is not misread as hung.
//
// It deliberately never creates or resurrects an entry: a Stop/SessionEnd
// event already dropped a finished agent from the map (trackProgress above),
// and a transcript write observed after that point must not revive it as a
// hang candidate — the agent is gone, not silent.
func recordAgentOutput(progress map[string]AgentProgress, agentName string, at time.Time) {
	p, ok := progress[agentName]
	if !ok {
		return
	}
	p.LastProgressAt = at
	progress[agentName] = p
}
