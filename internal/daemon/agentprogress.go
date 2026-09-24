package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

// Idle budgets after which an agent that is still running is treated as hung.
//
// They are deliberately two numbers, not one rule with a single input: "no
// event for N minutes" means something different depending on whether the
// agent has outstanding work. Waiting on a local tool call, waiting on a
// dispatched subagent and waiting on the model are the same thing from the
// outside — outstanding work, from three sources — so any one of them earns
// the generous bound; genuine idleness, with none, gets the short one and the
// short bound is finally telling the truth (SC-3074, SC-4900).
//
// A single fixed timeout cannot serve both, which is exactly why the wall-clock
// grace it replaces was wrong in both directions at once.
var (
	// IdleGrace bounds silence when the agent has no outstanding work at all.
	IdleGrace = 3 * time.Minute
	// WorkingIdleGrace bounds silence while the agent has outstanding work —
	// inside a tool call, waiting on a subagent, or waiting on a model
	// response. Generous on purpose:
	// killing a running suite, or a run composing a long answer, is far worse
	// than noticing a genuine hang a few minutes later.
	WorkingIdleGrace = 30 * time.Minute
)

// ModelRequestState is what the daemon knows about an agent's traffic to the
// model, and it has three values because the daemon has three answers. The
// signal is best-effort: it needs the connection's IP to resolve to an agent
// name, and that mapping goes missing silently (a daemon replaced under a
// running agent, an inspect that returned no address, a warm relaunch). A
// boolean forced every one of those to answer "no" — the same value a
// genuinely idle agent gives — so losing the bookkeeping promoted the most
// destructive action available, killing live work after three minutes, to the
// default (SC-3853).
//
// The zero value is deliberately ModelRequestUnknown: anything that did not
// consult the proxy says so, and gets the generous budget.
type ModelRequestState int

const (
	// ModelRequestUnknown means the daemon could not answer — the agent's
	// connections resolve to no name, so its in-flight count means nothing.
	ModelRequestUnknown ModelRequestState = iota
	// ModelRequestNone means the daemon COULD answer and nothing is open.
	ModelRequestNone
	// ModelRequestOpen means a request is open: the agent is thinking.
	ModelRequestOpen
)

// String renders the state for logs and the fsm-where report.
//
//exhaustive:enforce
func (s ModelRequestState) String() string {
	switch s {
	case ModelRequestNone:
		return "none"
	case ModelRequestOpen:
		return "open"
	case ModelRequestUnknown:
		return "unknown"
	}
	return "unknown"
}

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
	// ModelRequest is what the daemon's own proxy knows about this agent's
	// traffic to the model — open, none, or unknown. It is what tells a
	// thinking agent (which emits no hook event and streams no transcript
	// output during extended reasoning) apart from a genuinely hung one, and
	// it replaces the disproven transcript-mtime heartbeat (SC-3074). Unknown
	// is a real answer, not a missing one (SC-3853).
	ModelRequest ModelRequestState
	// Blocked reports the agent is waiting on a human (a permission prompt).
	// That is neither progress nor a hang — it needs an answer, not a retry.
	Blocked bool
	// Subagents is how many dispatches this agent is waiting on. It is counted
	// rather than derived from InsideTool because a subagent's hook events
	// carry its PARENT's agent name, session and run id — nothing in the event
	// separates them — so the subagent's own PostToolUse erases the parent's
	// record of being inside the Agent call that spawned it. Counting the
	// SubagentStart/SubagentStop brackets keeps the parent's outstanding work
	// visible for exactly as long as it is outstanding (SC-4900).
	Subagents int
}

// hasOutstandingWork reports whether the agent has work in flight of any of
// three kinds — a local tool call, a request to the model, or a subagent it
// dispatched — that no event can arrive to signal until it completes (SC-3074,
// SC-4900).
//
// An UNKNOWN model-request state counts as work in flight. That is the same
// rule stageStalled states for the other input: absent evidence is never read
// as evidence of death, because killing live work on a bookkeeping failure is
// the one direction this must never fail in (SC-3853).
func (p AgentProgress) hasOutstandingWork() bool {
	return p.InsideTool || p.Subagents > 0 || p.ModelRequest != ModelRequestNone
}

// OutstandingWork names what the agent had in flight, for the record a reap
// leaves on the ticket: "none", or the tool call, dispatch count and model
// request state that bought it the generous budget. It exists so a reap can
// be read back later against the inputs it was judged on — a silence under
// the generous budget with a request open and a silence under the short one
// with nothing outstanding are different findings that one idle figure would
// hide (SC-5329).
func (p AgentProgress) OutstandingWork() string {
	var parts []string
	if p.InsideTool {
		tool := p.Tool
		if tool == "" {
			tool = "a tool call"
		}
		parts = append(parts, "inside "+tool)
	}
	if p.Subagents > 0 {
		parts = append(parts, fmt.Sprintf("%d subagent(s) dispatched", p.Subagents))
	}
	if p.ModelRequest != ModelRequestNone {
		parts = append(parts, "model request "+p.ModelRequest.String())
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
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
	// ModelRequest is left at its zero value (unknown) on purpose: the hook
	// stream cannot see the proxy, and only the probe that consults it may
	// claim "none" (SC-3853).
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
	case hookevents.EventSubagentStart:
		// The parent is now inside a dispatch whose only other sign of life is
		// the subagent's own events — which arrive under the parent's name and
		// would otherwise clear InsideTool. Depth, not a flag: a subagent may
		// dispatch its own.
		p.Subagents++
		p.Blocked = false
	case hookevents.EventSubagentStop:
		// Floored because a dropped Stop must not push the count negative and
		// silently cancel a later dispatch. A Stop that never arrives leaves
		// the generous budget in place until the run ends and the entry is
		// dropped — the safe direction, the same one SC-3853 chose.
		if p.Subagents > 0 {
			p.Subagents--
		}
		p.Blocked = false
	case "Notification":
		// Claude is asking for permission: the agent is waiting on a human.
		p.Blocked = true
	default:
		p.Blocked = false
	}
	progress[evt.AgentName] = p
}
