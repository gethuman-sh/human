package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// planCard is the durable-pass shape of the stranded planning handoff: the
// stage started, the planner attached its plan, and no [human:plan-ready]
// followed — and, unlike the live-watcher test, the live exit event never
// arrived at all (a daemon restart, a dropped hook).
func planCard(started, plan time.Time) []ReconcileCard {
	return []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(PlanningStartedHeader, started),
		cmt(PlanCommentHeader+"\n\n# Implementation Plan", plan),
	}}}
}

// The durable twin: a planning card whose plan is attached and whose handoff
// never landed is completed, not reddened — the case that reaches this pass when
// the live watcher missed the exit (SC-5090, the SC-3149 symmetry rule).
func TestReconcileStuckRunning_PlanAttachedCompletesTheHandoff(t *testing.T) {
	now := time.Now()
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	cards := planCard(now.Add(-StuckRunningGrace-time.Minute), now.Add(-StuckRunningGrace))
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted),
			Retry: retry, DaemonID: "d1"}, now)

	require.Equal(t, 0, n, "a completed handoff is not a reddening")
	require.Len(t, posted, 1)
	require.True(t, strings.HasPrefix(strings.TrimSpace(posted[0].Body), PlanReadyHeader))
	require.Contains(t, posted[0].Body, planHandoffCompletedSentinel)
	require.Empty(t, relaunched, "nothing failed, so nothing is re-planned")
}

// A planner still alive and about to post its own handoff must not be
// pre-empted: the live agent is genuinely working, so the liveness verdict
// never reaches the point of completing anything on its behalf.
func TestReconcileStuckRunning_PlanAttachedLiveAgentIsNotRaced(t *testing.T) {
	now := time.Now()
	var posted []struct{ Key, Body string }
	cards := planCard(now.Add(-StuckRunningGrace-time.Minute), now.Add(-StuckRunningGrace))

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents("board-SC-1-planning"), PostFailed: capturingPoster(&posted),
			Progress: progressAt(now, false, false), DaemonID: "d1"}, now)

	require.Equal(t, 0, n)
	require.Empty(t, posted, "a planner still working and about to post its own handoff must not be pre-empted")
}

// The silenced case this pass can also reach: a planning agent is still LIVE
// but has gone quiet past its idle budget, so hungLiveAgent stops it before
// stuckCardLivenessVerdict returns. The plan is already attached, so the
// handoff is completed rather than the card reddened — but the completion
// must say plainly that the run was stopped, not that it exited, and the
// SC-2447/SC-3074 silence-reap trail must still land on the thread rather
// than being silently swallowed by the completion (SC-5090).
func TestReconcileStuckRunning_PlanAttachedSilencedLiveAgentStillRecordsTheReap(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var stopped []string
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}
	cards := planCard(now.Add(-StuckRunningGrace-time.Hour), now.Add(-StuckRunningGrace-time.Minute))

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents("board-SC-1-planning"), PostFailed: capturingPoster(&posted),
			Retry: retry, Progress: progressAt(now.Add(-IdleGrace-time.Minute), false, false),
			StopAgent: func(name string) error { stopped = append(stopped, name); return nil }, DaemonID: "d1"}, now)

	require.Equal(t, 0, n, "a completed handoff is not a reddening, even when this pass had to stop the agent")
	require.Equal(t, []string{"board-SC-1-planning"}, stopped,
		"the hung planner must still be stopped before the handoff is completed on its behalf")
	require.Empty(t, relaunched, "the plan is already attached, so nothing is re-planned")
	require.Len(t, posted, 1)
	require.True(t, strings.HasPrefix(strings.TrimSpace(posted[0].Body), PlanReadyHeader))
	require.Contains(t, posted[0].Body, planHandoffCompletedSentinel)
	require.NotContains(t, posted[0].Body, "exited before posting this handoff",
		"the run did not exit — this pass stopped it — so the body must not claim otherwise")
	require.Contains(t, posted[0].Body, "idle:",
		"the silence-reap observation must still land on the thread, not be lost by the completion")
}
