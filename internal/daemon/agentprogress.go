package daemon

import (
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

// Idle budgets after which an agent that is still running is treated as hung.
//
// They are deliberately two numbers, not one rule with a single input: "no
// event for N minutes" means something different depending on whether the
// agent has outstanding work. Waiting on a local tool call and waiting on the
// model are the same thing from the outside — outstanding work, from two
// sources — so either one earns the generous bound; genuine idleness, with
// neither, gets the short one and the short bound is finally telling the
// truth (SC-3074).
//
// A single fixed timeout cannot serve both, which is exactly why the wall-clock
// grace it replaces was wrong in both directions at once.
var (
	// IdleGrace bounds silence when the agent has no outstanding work at all.
	IdleGrace = 3 * time.Minute
	// WorkingIdleGrace bounds silence while the agent has outstanding work —
	// inside a tool call or waiting on a model response. Generous on purpose:
	// killing a running suite, or a run composing a long answer, is far worse
	// than noticing a genuine hang a few minutes later.
	WorkingIdleGrace = 30 * time.Minute
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
	// LastEvent is the hook event name that produced LastEventAt.
	LastEvent string
	// Tool is the tool currently executing, when InsideTool is set.
	Tool string
	// InsideTool reports a PreToolUse with no matching PostToolUse yet: the
	// agent is waiting on a command, not idle.
	InsideTool bool
	// OutstandingModelRequest reports a request sent to the model through the
	// daemon's own proxy that has not yet completed. This is a positive sign
	// of life the daemon holds directly — no decryption, no watching bytes,
	// no cooperation from the agent required — and it is what tells a
	// thinking agent (which emits no hook event and streams no transcript
	// output during extended reasoning) apart from a genuinely hung one. It
	// replaces the disproven transcript-mtime heartbeat (SC-3074).
	OutstandingModelRequest bool
	// Blocked reports the agent is waiting on a human (a permission prompt).
	// That is neither progress nor a hang — it needs an answer, not a retry.
	Blocked bool
}

// hasOutstandingWork reports whether the agent has work in flight of either
// kind — a local tool call or a request to the model — that no event can
// arrive to signal until it completes. The two are the same thing from the
// outside: outstanding work, from two sources (SC-3074).
func (p AgentProgress) hasOutstandingWork() bool {
	return p.InsideTool || p.OutstandingModelRequest
}

// IdleBudget is how long this agent may stay silent before it counts as hung.
func (p AgentProgress) IdleBudget() time.Duration {
	if p.hasOutstandingWork() {
		return WorkingIdleGrace
	}
	return IdleGrace
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
	if hookevents.IsRunEnd(evt.EventName) {
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
