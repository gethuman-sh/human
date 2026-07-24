package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/stretchr/testify/require"
)

// The gap this closes: the live failure watcher relaunches a retryable stage,
// but it fires only on an exit hook. An agent that dies with no hook — a daemon
// restart, a dropped event — was reached only by reconcile, which reddened the
// card and stopped. Reconcile now runs the same bounded relaunch, so a silently
// dead stage recovers here too.
func TestReconcileStuckRunning_RelaunchesAfterReddening(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	attempts := 0
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false }, // died silently, no record
		Attempts: func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), retry, nil, nil, "d1", now, zerolog.Nop())

	require.Equal(t, 1, n, "the card is reddened")
	require.Len(t, posted, 1, "the failed marker is the trail record")
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched,
		"a silently-dead stage is relaunched, not just reddened")
}

// 1290: a card parked on an open [human:options] block for its OWN running
// stage is a deliberate human pause, not a hang — the live failure watcher
// already treats this as a clean pause via stagePausedOnOptions
// (TestRunBoardFailureWatch_OpenPlanningOptionsIsCleanPause). The durable
// reconcile pass lacked the twin guard, so it posted a spurious
// implementation-failed marker and looped re-planning forever. This must post
// NO failed marker and relaunch nothing.
func TestReconcileStuckRunning_OpenSameStageOptionsIsCleanPause(t *testing.T) {
	now := time.Unix(10_000, 0)
	started := now.Add(-StuckRunningGrace - time.Minute)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, started),
			cmt("[human:options]\nstage: implementation\ncontext: pick storage\n1: sqlite\n2: files", started.Add(time.Second)),
		},
	}}
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), retry, nil, nil, "d1", now, zerolog.Nop())

	require.Equal(t, 0, n, "an open same-stage options block is a clean pause, not a hang")
	require.Empty(t, posted, "no failed marker for a card parked on its own decision")
	require.Empty(t, relaunched, "nothing to relaunch — the card is not dead, it is waiting on a human")
}

// 1290 stage-precision companion: an open options block naming a DIFFERENT
// stage does not belong to this run, so a genuine hang must still be
// reddened — the guard must not swallow unrelated dead ends.
func TestReconcileStuckRunning_OpenOptionsForOtherStageStillReddens(t *testing.T) {
	now := time.Unix(10_000, 0)
	started := now.Add(-StuckRunningGrace - time.Minute)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, started),
			cmt("[human:options]\nstage: planning\ncontext: unrelated\n1: a\n2: b", started.Add(time.Second)),
		},
	}}
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), retry, nil, nil, "d1", now, zerolog.Nop())

	require.Equal(t, 1, n, "an options block for a different stage does not excuse a genuine hang")
	require.Len(t, posted, 1, "the failed marker is the trail record")
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched, "the hang is still relaunched under the bounded budget")
}

// The shared budget bounds both paths together: once the count is spent, the
// relaunch stops even though the card keeps reddening.
func TestReconcileStuckRunning_RelaunchRespectsTheBudget(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 3, nil }, // already past the cap
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), retry, nil, nil, "d1", now, zerolog.Nop())

	require.Equal(t, 1, n, "the card is still reddened for a human")
	require.Empty(t, relaunched, "a spent budget stops the automatic relaunch")
}
