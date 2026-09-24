package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// SC-5396: the done stage runs three differently named agents and none of them
// is board-<key>-done, so the sweep probed a name nothing launches and read
// "nobody is there" for every deploy fixer. On this repository a fixer's test
// run alone outlasts the 15-minute grace, so this redded every conflict repair
// and charged a retry against a container that was alive and working.
func TestReconcileStuckRunning_SparesLiveDeployFixer(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(DeployFixStartedHeader+"\nbranch: feat/x", now.Add(-20*time.Minute)),
	}}}
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	attemptsCalled := false
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { attemptsCalled = true; return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}
	deps := ReconcileDeps{
		LiveAgents: liveAgents(agentNameFor("SC-1", deployFixAgentStage)),
		PostFailed: capturingPoster(&posted),
		Retry:      retry,
		StopAgent:  func(string) error { t.Fatal("a live fixer must not be stopped"); return nil },
		DaemonID:   "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	assert.Equal(t, 0, n, "a live deploy fixer must not be reddened")
	assert.Empty(t, posted)
	assert.Empty(t, relaunched, "no relaunch on top of a working fixer")
	assert.False(t, attemptsCalled, "a live fixer must never be charged a retry for being slow")
}

// Bounded, not exempt: with the fixer genuinely gone the card is still redded
// on the charged vanished-agent path, exactly like every other stage.
func TestReconcileStuckRunning_RedsVanishedDeployFixer(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(DeployFixStartedHeader+"\nbranch: feat/x", now.Add(-20*time.Minute)),
	}}}
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	attemptsCalled := false
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { attemptsCalled = true; return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}
	deps := ReconcileDeps{
		LiveAgents: liveAgents(),
		PostFailed: capturingPoster(&posted),
		Retry:      retry,
		DaemonID:   "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	require.Equal(t, 1, n, "a genuinely vanished fixer is still reddened")
	require.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, DeployFailedHeader))
	assert.Contains(t, posted[0].Body, stuckRunningReason(BoardDoneStage))
	assert.True(t, attemptsCalled, "a vanished agent stays on the charged path")
	assert.Equal(t, []BoardStage{BoardDoneStage}, relaunched)
}

// The progress probe is what bounds the widened join: a done-stage container
// that is present but recording no new phase is judged hung, stopped, and the
// card redded on the UNCHARGED silence path — and the container stopped is the
// one that is actually alive, never the composed board-<key>-done.
func TestReconcileStuckRunning_StalledDeployFixerIsStoppedByItsRealName(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(DeployFixStartedHeader+"\nbranch: feat/x", now.Add(-20*time.Minute)),
	}}}
	var posted []struct{ Key, Body string }
	var stopped []string
	attemptsCalled := false
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { attemptsCalled = true; return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { return true, nil },
	}
	deps := ReconcileDeps{
		LiveAgents: liveAgents(agentNameFor("SC-1", deployFixAgentStage)),
		PostFailed: capturingPoster(&posted),
		Retry:      retry,
		Progress:   progressAt(now.Add(-IdleGrace-time.Minute), false, false),
		StopAgent:  func(name string) error { stopped = append(stopped, name); return nil },
		DaemonID:   "d1",
	}

	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	require.Equal(t, 1, n)
	require.Equal(t, []string{"board-SC-1-deployfix"}, stopped)
	assert.False(t, attemptsCalled, "a machine-chosen stop is not charged (SC-2447)")
	require.Len(t, posted, 1)
	assert.Contains(t, posted[0].Body, "not charged")
}

// reconcileOutage carries the identical join (board_reconcile.go:604): a
// done-stage card in outage with a live fixer must not be relaunched on top of it.
func TestReconcileOutage_SkipsWhileTheDeployFixerIsAlive(t *testing.T) {
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(DeployFixStartedHeader+"\nbranch: feat/x", now.Add(-30*time.Minute)),
		cmt(DeployOutageHeader+"\nthe forge was unreachable", now.Add(-20*time.Minute)),
	}}}
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}
	deps := ReconcileDeps{
		LiveAgents: liveAgents(agentNameFor("SC-1", deployFixAgentStage)),
		Retry:      retry,
		DaemonID:   "d1",
	}

	redriven, _ := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), deps, now)

	assert.Zero(t, redriven)
	assert.Empty(t, relaunched, "a live deploy fixer must not be relaunched on top of")
}
