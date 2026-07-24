package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// runningCard is an aged, still-running implementation card whose agent is
// alive — the shape a hung stage takes.
func runningCard(now time.Time) []ReconcileCard {
	return []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
}

func progressAt(last time.Time, insideTool, blocked bool) AgentProgressProbe {
	return func(string) (AgentProgress, bool) {
		return AgentProgress{LastEventAt: last, InsideTool: insideTool, Blocked: blocked}, true
	}
}

// The defect this fixes: a hung agent keeps its container alive, so the old
// liveness check skipped it forever and the hang was never detected at all.
func TestReconcileStuckRunning_HungAgentIsStoppedReddenedAndRelaunched(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var stopped []string
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	n := reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), retry,
		progressAt(now.Add(-ThinkingIdleGrace-time.Minute), false, false),
		func(name string) error { stopped = append(stopped, name); return nil },
		"d1", now, zerolog.Nop())

	require.Equal(t, 1, n, "the hung card is reddened")
	require.Equal(t, []string{"board-SC-1-implementation"}, stopped,
		"the hung agent must be stopped before anything relaunches")
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
}

// The other half: an agent that is genuinely working must never be killed,
// however long the stage has been running.
func TestReconcileStuckRunning_ActiveAgentIsLeftAlone(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var stopped []string

	n := reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), StageRetry{},
		progressAt(now.Add(-10*time.Second), false, false),
		func(name string) error { stopped = append(stopped, name); return nil },
		"d1", now, zerolog.Nop())

	require.Equal(t, 0, n)
	require.Empty(t, posted)
	require.Empty(t, stopped)
}

// A ten-minute Bash call emits nothing until it returns; that is work, not a
// hang, and the tool budget must cover it.
func TestReconcileStuckRunning_LongToolCallIsNotAHang(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var stopped []string

	n := reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), StageRetry{},
		progressAt(now.Add(-10*time.Minute), true, false), // inside a tool call
		func(name string) error { stopped = append(stopped, name); return nil },
		"d1", now, zerolog.Nop())

	require.Equal(t, 0, n, "a running suite must not be killed")
	require.Empty(t, stopped)
}

// Waiting on a permission prompt needs an answer, not a relaunch.
func TestReconcileStuckRunning_BlockedAgentIsNotHung(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var stopped []string

	n := reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), StageRetry{},
		progressAt(now.Add(-time.Hour), false, true), // blocked on a human
		func(name string) error { stopped = append(stopped, name); return nil },
		"d1", now, zerolog.Nop())

	require.Equal(t, 0, n)
	require.Empty(t, stopped)
}

// Absent evidence must never kill live work: an unknown agent (daemon restarted
// and lost its progress map) is left alone.
func TestReconcileStuckRunning_UnknownProgressLeavesLiveWorkAlone(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }

	unknown := func(string) (AgentProgress, bool) { return AgentProgress{}, false }
	n := reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), StageRetry{},
		unknown, func(string) error { return nil }, "d1", now, zerolog.Nop())

	require.Equal(t, 0, n)

	// Same when no probe is wired at all — previous behaviour, unchanged.
	n = reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), StageRetry{},
		nil, nil, "d1", now, zerolog.Nop())
	require.Equal(t, 0, n)
}

// If the hung agent cannot be stopped, the card must be left as-is rather than
// relaunched into a second agent on the same stage.
func TestReconcileStuckRunning_FailedStopDoesNotRelaunch(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	n := reconcileStuckRunning(context.Background(), runningCard(now),
		liveAgents("board-SC-1-implementation"), capturingPoster(&posted), retry,
		progressAt(now.Add(-time.Hour), false, false),
		func(string) error { return errors.New("docker unavailable") },
		"d1", now, zerolog.Nop())

	require.Equal(t, 0, n)
	require.Empty(t, relaunched, "never relaunch while the hung agent may still be running")
}
