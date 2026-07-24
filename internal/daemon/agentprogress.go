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
	// LastEventAt is when this agent last did anything observable.
	LastEventAt time.Time
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
func (p AgentProgress) Stalled(now time.Time) (bool, time.Duration) {
	idle := now.Sub(p.LastEventAt)
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
