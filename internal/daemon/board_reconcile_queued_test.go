package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// decidedCard is a card whose decision was answered `ago` before now and whose
// stage never started — the window this pass exists for.
func decidedCard(key string, stage BoardStage, now time.Time, ago time.Duration) ReconcileCard {
	answered := now.Add(-ago)
	return ReconcileCard{
		Key: key,
		Comments: []tracker.Comment{
			cmt(OptionsHeader+"\nstage: "+string(stage)+"\n1: rebuild\n2: leave it", answered.Add(-time.Minute)),
			cmt(OptionChosenHeader+" 1: rebuild", answered),
		},
	}
}

// queuedRetry is a StageRetry whose recorded exit is ExitNeedsInput — what the
// stage that RAISED the decision actually leaves behind, and the reason this
// pass cannot go through tryRelaunch.
func queuedRetry(relaunched *[]BoardStage, attempts *int) StageRetry {
	return StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitNeedsInput, true },
		Attempts: func(string, BoardStage) (int, error) { *attempts++; return *attempts, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { *relaunched = append(*relaunched, s); return true, nil },
	}
}

// The acceptance criterion of SC-3865: a card whose decision was answered and
// whose launch never happened does not sit there forever.
func TestReconcileQueuedLaunch_StartsAStageThatNeverLaunched(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{decidedCard("SC-1", BoardImplementation, now, time.Hour)}
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"}, now)

	require.Equal(t, 1, n, "the decided card is started")
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched, "and started at the stage the block named")
}

// The trap this pass had to route around: tryRelaunch classifies from the
// stage's last RECORDED exit, and the stage that raised the decision recorded
// ExitNeedsInput — "leave it for a human". Going through it would have refused
// to start exactly the cards this pass exists for, silently and forever. Pinned
// so a later simplification back onto tryRelaunch fails here rather than in
// production.
func TestReconcileQueuedLaunch_StartsDespiteANeedsInputExit(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{decidedCard("SC-1", BoardPlanning, now, time.Hour)}
	var relaunched []BoardStage
	attempts := 0
	retry := queuedRetry(&relaunched, &attempts)
	require.Equal(t, relaunchNone, classifyRelaunch(ExitNeedsInput, true),
		"precondition: this exit is the one tryRelaunch refuses to act on")

	n := reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	require.Equal(t, 1, n, "the answered decision supersedes the exit that asked the question")
	require.Equal(t, []BoardStage{BoardPlanning}, relaunched)
}

// ApplyOption records the choice and then launches, in one call. A tick landing
// inside that gap must not start a second agent for the same stage.
func TestReconcileQueuedLaunch_LeavesALaunchStillInFlight(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{decidedCard("SC-1", BoardImplementation, now, QueuedLaunchGrace/2)}
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"}, now)

	require.Equal(t, 0, n, "inside the grace the launch is presumed on its way")
	require.Empty(t, relaunched)
	require.Zero(t, attempts, "and nothing is charged for waiting")
}

// A live agent for the stage means the launch DID happen and has not posted its
// started marker yet — the same alive-guard the other passes use.
func TestReconcileQueuedLaunch_SkipsWhenTheStageAgentIsAlive(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{decidedCard("SC-1", BoardImplementation, now, time.Hour)}
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(agentNameFor("SC-1", BoardImplementation)), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"}, now)

	require.Equal(t, 0, n, "a live agent means the launch is not missing")
	require.Empty(t, relaunched)
}

// Bounded, so a stage that cannot be launched cannot be launched forever.
func TestReconcileQueuedLaunch_StopsAtTheAttemptCap(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{decidedCard("SC-1", BoardImplementation, now, time.Hour)}
	var relaunched []BoardStage
	attempts := 0
	retry := queuedRetry(&relaunched, &attempts)

	for range 4 {
		reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)
	}

	require.Len(t, relaunched, 2, "DefaultStageRetries bounds it — the card is left for a person after that")
}

// A refusal that starts nothing (the plan gate) must not spend the budget a
// genuine missing launch is entitled to (SC-2989), and must not be counted as a
// recovery.
func TestReconcileQueuedLaunch_DoesNotChargeARefusedLaunch(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{decidedCard("SC-1", BoardImplementation, now, time.Hour)}
	uncounted := 0
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitNeedsInput, true },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(string, BoardStage) (bool, error) { return false, nil },
		Uncount:  func(string, BoardStage) { uncounted++ },
	}

	n := reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	require.Equal(t, 0, n, "nothing started, so nothing was recovered")
	require.Equal(t, 1, uncounted, "and the charged attempt is rolled back")
}

// Everything that is not a decided-and-unstarted card is none of this pass's
// business — most of all a running one, which the stuck pass owns.
func TestReconcileQueuedLaunch_IgnoresCardsThatAreNotQueued(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{
		{Key: "SC-running", Comments: []tracker.Comment{cmt(ImplementationStartedHeader, now.Add(-time.Hour))}},
		{Key: "SC-bare", Comments: nil},
		// Answered, and then the stage actually started: not queued any more.
		{Key: "SC-started", Comments: append(
			decidedCard("SC-started", BoardImplementation, now, time.Hour).Comments,
			cmt(ImplementationStartedHeader, now.Add(-time.Minute)))},
	}
	var relaunched []BoardStage
	attempts := 0

	n := reconcileQueuedLaunch(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: queuedRetry(&relaunched, &attempts), DaemonID: "d1"}, now)

	require.Equal(t, 0, n)
	require.Empty(t, relaunched)
}
