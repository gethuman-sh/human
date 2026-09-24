package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// youngRunningCard is a running implementation card one minute into its stage
// — far inside StuckRunningGrace, so only a recorded death may red it.
func youngRunningCard(now time.Time) []ReconcileCard {
	return []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-2*time.Minute))},
	}}
}

func stoppedAgents(m map[string]time.Time) StoppedAgentLister {
	return func() (map[string]time.Time, error) { return m, nil }
}

func chargedRetry(attemptsCalled *bool, relaunched *[]BoardStage) StageRetry {
	return StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { *attemptsCalled = true; return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { *relaunched = append(*relaunched, s); return true, nil },
	}
}

// The defect: a SIGKILLed agent is written as stopped within seconds, but the
// pass only judged cards past StuckRunningGrace, so the card sat fifteen
// minutes on a death the machine already knew about (SC-5327). A stop recorded
// after the stage began is an exit event: reddened and relaunched on this
// tick, on the charged path a real death takes.
func TestReconcileStuckRunning_RecordedDeathSkipsTheGrace(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	var attemptsCalled bool
	var relaunched []BoardStage
	deps := ReconcileDeps{
		LiveAgents:    liveAgents(),
		StoppedAgents: stoppedAgents(map[string]time.Time{"board-SC-1-implementation": now.Add(-time.Minute)}),
		PostFailed:    capturingPoster(&posted),
		Retry:         chargedRetry(&attemptsCalled, &relaunched),
		DaemonID:      "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(youngRunningCard(now), alwaysReachable), deps, now)

	require.Equal(t, 1, n, "a recorded death is reddened without waiting out the grace")
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
	require.True(t, attemptsCalled, "a recorded death is a real death and stays charged")
	require.Len(t, posted, 1)
	require.NotContains(t, posted[0].Body, "not charged")
}

// A stop older than the stage belongs to an earlier run of the same stage whose
// exit was already handled; it says nothing about the run the card shows.
func TestReconcileStuckRunning_StopBeforeStageEntryKeepsTheGrace(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	deps := ReconcileDeps{
		LiveAgents:    liveAgents(),
		StoppedAgents: stoppedAgents(map[string]time.Time{"board-SC-1-implementation": now.Add(-10 * time.Minute)}),
		PostFailed:    capturingPoster(&posted),
		Retry:         StageRetry{Max: 2},
		DaemonID:      "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(youngRunningCard(now), alwaysReachable), deps, now)

	require.Equal(t, 0, n)
	require.Empty(t, posted)
}

// A live agent is never a recorded death, whatever an older record says: the
// live listing is the newer fact.
func TestReconcileStuckRunning_AliveAgentKeepsTheGraceDespiteOldStopRecord(t *testing.T) {
	now := time.Unix(100_000, 0)
	var posted []struct{ Key, Body string }
	deps := ReconcileDeps{
		LiveAgents:    liveAgents("board-SC-1-implementation"),
		StoppedAgents: stoppedAgents(map[string]time.Time{"board-SC-1-implementation": now.Add(-time.Minute)}),
		PostFailed:    capturingPoster(&posted),
		Retry:         StageRetry{Max: 2},
		Progress:      progressAt(now, true, false),
		StopAgent:     func(string) error { t.Fatal("a live, working agent must not be stopped"); return nil },
		DaemonID:      "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(youngRunningCard(now), alwaysReachable), deps, now)

	require.Equal(t, 0, n)
	require.Empty(t, posted)
}

// No lister, or a lister that fails, is absent evidence: the card keeps the
// ordinary grace rather than being judged on a fact that could not be read.
func TestRecordedDeath_AbsentEvidenceIsNotADeath(t *testing.T) {
	now := time.Unix(100_000, 0)
	entered := now.Add(-2 * time.Minute)
	alive := map[string]struct{}{}

	died, _ := recordedDeath(ReconcileDeps{}, []string{"board-SC-1-implementation"}, alive, entered)
	require.False(t, died, "nil lister disables the shortcut")

	failing := ReconcileDeps{StoppedAgents: func() (map[string]time.Time, error) { return nil, errors.New("boom") }}
	died, _ = recordedDeath(failing, []string{"board-SC-1-implementation"}, alive, entered)
	require.False(t, died, "a lister error is not evidence")

	unnamed := ReconcileDeps{StoppedAgents: stoppedAgents(map[string]time.Time{"board-SC-2-implementation": now})}
	died, _ = recordedDeath(unnamed, []string{"board-SC-1-implementation"}, alive, entered)
	require.False(t, died, "a record for another agent says nothing about this one")

	noEntry := ReconcileDeps{StoppedAgents: stoppedAgents(map[string]time.Time{"board-SC-1-implementation": now})}
	died, _ = recordedDeath(noEntry, []string{"board-SC-1-implementation"}, alive, time.Time{})
	require.False(t, died, "without a stage entry time the stop cannot be placed in this stage")

	died, at := recordedDeath(noEntry, []string{"board-SC-1-implementation"}, alive, entered)
	require.True(t, died)
	require.Equal(t, now, at)
}

// The regression: AdvancePRLoop posts [human:pr-review-passed] and then calls
// DeployBranch SYNCHRONOUSLY for the CI gate (up to 45 minutes) — a window
// with no board agent at all. The reviewer's container is torn down by its own
// Stop hook after that marker post, so its ordinary, successful exit is
// recorded as a stop that postdates StageEnteredAt exactly like a real death
// would be. Without deployEngineActive, recordedDeath read that as evidence
// the done stage died, skipped stuckPastGrace (and its DeployRunProbe branch)
// entirely, and reddened a merge that was still running (SC-5396).
func TestReconcileStuckRunning_DeployEngineActiveSparesAStaleReviewerStop(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", now.Add(-40*time.Minute)),
		cmt(PRReviewStartedHeader+"\npr: u\nnumber: 7\nbranch: feat/x", now.Add(-30*time.Minute)),
		cmt(PRReviewPassedHeader+"\nbranch: feat/x", now.Add(-10*time.Minute)),
	}}}
	var posted []struct{ Key, Body string }
	deps := ReconcileDeps{
		LiveAgents: liveAgents(),
		StoppedAgents: stoppedAgents(map[string]time.Time{
			agentNameFor("SC-1", prReviewAgentStage): now.Add(-9 * time.Minute), // the reviewer's ordinary exit, after pr-review-passed
		}),
		PostFailed: capturingPoster(&posted),
		Retry:      StageRetry{Max: 2},
		DeployRun:  func(string) (time.Time, bool) { return now.Add(-5 * time.Minute), true },
		DaemonID:   "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	require.Equal(t, 0, n, "the CI gate is still running on this machine's own clock; the stale reviewer stop must not red it")
	require.Empty(t, posted)
}

// Same shape for the deploy fixer: AdvanceDeployFix's ExitDone path also calls
// DeployBranch synchronously with no started marker in between, so a resolved
// fixer's ordinary exit must not be read as this window's death either.
func TestReconcileStuckRunning_DeployEngineActiveSparesAStaleDeployFixerStop(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(DeployFixStartedHeader+"\nbranch: feat/x", now.Add(-30*time.Minute)),
	}}}
	var posted []struct{ Key, Body string }
	deps := ReconcileDeps{
		LiveAgents: liveAgents(),
		StoppedAgents: stoppedAgents(map[string]time.Time{
			agentNameFor("SC-1", deployFixAgentStage): now.Add(-9 * time.Minute), // resolved and exited normally
		}),
		PostFailed: capturingPoster(&posted),
		Retry:      StageRetry{Max: 2},
		DeployRun:  func(string) (time.Time, bool) { return now.Add(-5 * time.Minute), true },
		DaemonID:   "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	require.Equal(t, 0, n, "the retry deploy is still running on this machine's own clock")
	require.Empty(t, posted)
}

// Without an active DeployRun registration (a peer daemon's run, or one lost
// to a restart) the stale-stop evidence still counts — deployEngineActive must
// narrow the shortcut, not disable it outright.
func TestReconcileStuckRunning_RecordedDeathStillFiresWithoutAnActiveDeployRun(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x", now.Add(-40*time.Minute)),
		cmt(PRReviewStartedHeader+"\npr: u\nnumber: 7\nbranch: feat/x", now.Add(-30*time.Minute)),
		cmt(PRReviewPassedHeader+"\nbranch: feat/x", now.Add(-10*time.Minute)),
	}}}
	var posted []struct{ Key, Body string }
	var attemptsCalled bool
	var relaunched []BoardStage
	deps := ReconcileDeps{
		LiveAgents: liveAgents(),
		StoppedAgents: stoppedAgents(map[string]time.Time{
			agentNameFor("SC-1", prReviewAgentStage): now.Add(-9 * time.Minute),
		}),
		PostFailed: capturingPoster(&posted),
		Retry:      chargedRetry(&attemptsCalled, &relaunched),
		DeployRun:  func(string) (time.Time, bool) { return time.Time{}, false },
		DaemonID:   "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	require.Equal(t, 1, n, "with no in-process run registered, the recorded stop is still this pass's only evidence")
	require.Len(t, posted, 1)
}
