package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

func evt(name, agent, tool string, at time.Time) hookevents.Event {
	return hookevents.Event{EventName: name, AgentName: agent, ToolName: tool, Timestamp: at}
}

// A tool call in flight is not idleness: no event can arrive until the command
// returns, so the agent gets the long budget.
func TestAgentProgress_InsideToolUsesTheLongBudget(t *testing.T) {
	progress := map[string]AgentProgress{}
	start := time.Unix(1000, 0)
	trackProgress(progress, evt("PreToolUse", "board-SC-1-implementation", "Bash", start))

	p := progress["board-SC-1-implementation"]
	require.True(t, p.InsideTool)
	require.Equal(t, WorkingIdleGrace, p.IdleBudget())

	// Ten minutes into a test suite is normal work, not a hang.
	stalled, _ := p.Stalled(start.Add(10 * time.Minute))
	require.False(t, stalled)

	stalled, idle := p.Stalled(start.Add(WorkingIdleGrace + time.Minute))
	require.True(t, stalled, "past the tool budget it is hung")
	require.Greater(t, idle, WorkingIdleGrace)
}

// Between tool calls a model acts within seconds, so silence is abnormal fast.
func TestAgentProgress_ThinkingUsesTheShortBudget(t *testing.T) {
	progress := map[string]AgentProgress{}
	start := time.Unix(1000, 0)
	trackProgress(progress, evt("PreToolUse", "a", "Bash", start))
	trackProgress(progress, evt("PostToolUse", "a", "Bash", start.Add(time.Minute)))

	p := progress["a"]
	require.False(t, p.InsideTool, "the tool finished")
	require.Equal(t, IdleGrace, p.IdleBudget())

	stalled, _ := p.Stalled(start.Add(time.Minute + IdleGrace + time.Second))
	require.True(t, stalled)
}

// The whole point: a long stage that keeps working never looks hung, however
// long it runs. Wall-clock duration must not enter the decision.
func TestAgentProgress_LongRunningButActiveIsNeverStalled(t *testing.T) {
	progress := map[string]AgentProgress{}
	now := time.Unix(1000, 0)

	// Three hours of steady tool calls.
	for i := 0; i < 180; i++ {
		now = now.Add(time.Minute)
		trackProgress(progress, evt("PreToolUse", "a", "Bash", now))
		trackProgress(progress, evt("PostToolUse", "a", "Bash", now.Add(2*time.Second)))
	}

	p := progress["a"]
	stalled, _ := p.Stalled(now.Add(30 * time.Second))
	require.False(t, stalled, "a working agent is never stalled regardless of total runtime")
}

// SC-3074: a run waiting on the model — no hook event, between tool calls, but
// with a request sent to the model that has not come back — is WORKING, not hung.
func TestAgentProgress_OutstandingModelRequestIsNeverStalled(t *testing.T) {
	now := time.Unix(100_000, 0)
	p := AgentProgress{
		LastEventAt:             now.Add(-4 * time.Minute),
		InsideTool:              false,
		OutstandingModelRequest: true,
	}
	require.Equal(t, WorkingIdleGrace, p.IdleBudget())
	stalled, _ := p.Stalled(now)
	require.False(t, stalled, "a run waiting on the model is working, not hung")
}

// With NEITHER a hook event NOR outstanding work of any kind for longer than
// IdleGrace, the agent really has gone silent and must still be caught — the
// short budget still bounds total silence.
func TestAgentProgress_NoOutstandingWorkIsStalled(t *testing.T) {
	now := time.Unix(100_000, 0)
	p := AgentProgress{
		LastEventAt: now.Add(-4 * time.Minute),
	}

	stalled, idle := p.Stalled(now)
	require.True(t, stalled, "total silence past the budget is still a hang")
	require.Greater(t, idle, IdleGrace)
}

// An agent waiting on a permission prompt needs an answer, not a relaunch —
// retrying it would discard the question.
func TestAgentProgress_BlockedIsNotStalled(t *testing.T) {
	progress := map[string]AgentProgress{}
	start := time.Unix(1000, 0)
	trackProgress(progress, evt("Notification", "a", "", start))

	p := progress["a"]
	require.True(t, p.Blocked)

	stalled, _ := p.Stalled(start.Add(time.Hour))
	require.False(t, stalled, "blocked on a human is not a hang")
}

func TestAgentProgress_BlockedClearsOnNextAction(t *testing.T) {
	progress := map[string]AgentProgress{}
	start := time.Unix(1000, 0)
	trackProgress(progress, evt("Notification", "a", "", start))
	trackProgress(progress, evt("PostToolUse", "a", "Bash", start.Add(time.Minute)))

	require.False(t, progress["a"].Blocked, "the human answered and the agent moved on")
}

// A finished agent must not linger as a hang candidate.
func TestAgentProgress_TerminalEventsDropTheAgent(t *testing.T) {
	for _, ending := range []string{"Stop", "SessionEnd", "StopFailure"} {
		progress := map[string]AgentProgress{}
		trackProgress(progress, evt("PreToolUse", "a", "Bash", time.Unix(1000, 0)))
		trackProgress(progress, evt(ending, "a", "", time.Unix(1001, 0)))
		require.NotContains(t, progress, "a", "%s must clear the agent", ending)
	}
}

func TestAgentProgress_IgnoresEventsWithNoAgent(t *testing.T) {
	progress := map[string]AgentProgress{}
	trackProgress(progress, evt("PreToolUse", "", "Bash", time.Unix(1000, 0)))
	require.Empty(t, progress)
}

// The store must keep progress outside the event ring: a per-session cap of 200
// evicts events, and a quiet-but-working agent whose last event aged out would
// be misread as hung — the one direction this must never fail in.
func TestHookEventStore_ProgressSurvivesRingEviction(t *testing.T) {
	s := NewHookEventStore()
	at := time.Unix(1000, 0)
	s.Append(evt("PreToolUse", "board-SC-1-implementation", "Bash", at))

	// Flood the same session well past its cap.
	for i := 0; i < maxHookEventsPerSession+50; i++ {
		s.Append(hookevents.Event{EventName: "PostToolUse", SessionID: "s1", Timestamp: at})
	}

	p, ok := s.AgentProgress("board-SC-1-implementation")
	require.True(t, ok, "progress must outlive ring eviction")
	require.True(t, p.InsideTool)
	require.Equal(t, at, p.LastEventAt)
}

func TestHookEventStore_UnknownAgentIsNotKnown(t *testing.T) {
	s := NewHookEventStore()
	_, ok := s.AgentProgress("board-SC-9-planning")
	require.False(t, ok)
}
