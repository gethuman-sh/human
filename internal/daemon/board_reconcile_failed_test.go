package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func failedCard(key string, stage BoardStage, now time.Time, ago time.Duration, extra ...tracker.Comment) ReconcileCard {
	var started, failed string
	switch stage {
	case BoardPlanning:
		started, failed = PlanningStartedHeader, PlanningFailedHeader
	case BoardVerification:
		started, failed = ReviewStartedHeader, ReviewFailedHeader
	default:
		started, failed = ImplementationStartedHeader, ImplementationFailedHeader
	}
	comments := []tracker.Comment{
		cmt(started, now.Add(-ago-time.Hour)),
		cmt(failed+"\nreason: the container died", now.Add(-ago)),
	}
	return ReconcileCard{Key: key, Comments: append(comments, extra...)}
}

func recoveryRetry(outcome StageExit, recorded bool, relaunched *[]BoardStage, attempts *int, launch func() (bool, error)) StageRetry {
	if launch == nil {
		launch = func() (bool, error) { return true, nil }
	}
	return StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return outcome, recorded },
		Attempts: func(string, BoardStage) (int, error) { *attempts++; return *attempts, nil },
		Uncount:  func(string, BoardStage) { *attempts-- },
		Relaunch: func(_ string, s BoardStage) (bool, error) {
			ok, err := launch()
			if ok {
				*relaunched = append(*relaunched, s)
			}
			return ok, err
		},
	}
}

func resetRecoveryBackoff(t *testing.T) {
	t.Helper()
	failedRecoveryBackoff.reset()
	t.Cleanup(failedRecoveryBackoff.reset)
}

// The acceptance of SC-5170: a failed stage whose exit event was lost is
// relaunched by the durable pass, through the same charged policy the live
// path uses, once the live path has had its chance.
func TestReconcileFailedStages_RelaunchesAFailedStageTheLivePathMissed(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, 10*time.Minute)}
	var relaunched []BoardStage
	attempts := 0
	retry := recoveryRetry("", false, &relaunched, &attempts, nil)

	n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	require.Equal(t, 1, n)
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
	require.Equal(t, 1, attempts, "a real launch is charged, exactly like the live path")
}

// Within the grace the failure is the live path's to handle; the pass must not
// race it onto the same stage.
func TestReconcileFailedStages_LeavesAFreshFailureToTheLivePath(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, time.Minute)}
	var relaunched []BoardStage
	attempts := 0

	n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: recoveryRetry("", false, &relaunched, &attempts, nil), DaemonID: "d1"}, now)

	require.Zero(t, n)
	require.Empty(t, relaunched)
}

// Each of these is a card the machine must not touch: the deploy has its own
// recovery, a live agent is the relaunch already happened, an open decision is
// a person's, a silence-reap give-up is the machine's own stated refusal, and
// a failure past the bound is a person's.
func TestReconcileFailedStages_LeavesWhatItMustLeave(t *testing.T) {
	now := time.Unix(100_000, 0)
	for _, tc := range []struct {
		name  string
		card  ReconcileCard
		alive LiveAgentLister
	}{
		{"done-stage failure", ReconcileCard{Key: "SC-1", Comments: []tracker.Comment{
			cmt(DeployStartedHeader, now.Add(-time.Hour)),
			cmt(DeployFailedHeader+"\nreason: CI red", now.Add(-10*time.Minute)),
		}}, liveAgents()},
		{"agent alive", failedCard("SC-1", BoardImplementation, now, 10*time.Minute), liveAgents("board-SC-1-implementation")},
		{"open decision", failedCard("SC-1", BoardImplementation, now, 10*time.Minute,
			cmt("[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", now.Add(-9*time.Minute))), liveAgents()},
		{"silence-reap give-up", ReconcileCard{Key: "SC-1", Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(ImplementationFailedHeader+"\nreason: "+silenceReapGiveUpReason(BoardImplementation, 3), now.Add(-10*time.Minute)),
		}}, liveAgents()},
		{"past the bound", failedCard("SC-1", BoardImplementation, now, FailedRecoveryBound+time.Hour), liveAgents()},
		{"standing plan-stuck escalation", ReconcileCard{Key: "SC-1", Comments: []tracker.Comment{
			cmt(PlanningStartedHeader, now.Add(-2*time.Hour)),
			cmt(planStuckBody(PlanRedriveBound, cmt("", now.Add(-2*time.Hour))), now.Add(-10*time.Minute)),
		}}, liveAgents()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetRecoveryBackoff(t)
			var relaunched []BoardStage
			attempts := 0
			n := reconcileFailedStages(context.Background(), takeoverSet([]ReconcileCard{tc.card}, alwaysReachable), ReconcileDeps{LiveAgents: tc.alive, Retry: recoveryRetry("", false, &relaunched, &attempts, nil), DaemonID: "d1"}, now)
			require.Zero(t, n)
			require.Empty(t, relaunched)
			require.Zero(t, attempts)
		})
	}
}

// The policy's own rules apply unchanged: a deliberate needs-human-work stop is
// not relaunched, a needs-input with no open question is.
func TestReconcileFailedStages_UsesTheRetryPolicysRules(t *testing.T) {
	now := time.Unix(100_000, 0)
	for _, tc := range []struct {
		exit StageExit
		want int
	}{{ExitNeedsHumanWork, 0}, {ExitNeedsInput, 1}, {ExitRetryable, 1}} {
		t.Run(string(tc.exit), func(t *testing.T) {
			resetRecoveryBackoff(t)
			cards := []ReconcileCard{failedCard("SC-1", BoardPlanning, now, 10*time.Minute)}
			var relaunched []BoardStage
			attempts := 0
			n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: recoveryRetry(tc.exit, true, &relaunched, &attempts, nil), DaemonID: "d1"}, now)
			require.Equal(t, tc.want, n)
		})
	}
}

// A relaunch that starts nothing is retried with backoff, not once per tick:
// the second tick inside the wait does not ask again, the tick after it does,
// and a launch that finally starts clears the wait.
func TestReconcileFailedStages_BacksOffAfterARefusedLaunch(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, 10*time.Minute)}
	var relaunched []BoardStage
	attempts, launches := 0, 0
	refuse := true
	retry := recoveryRetry("", false, &relaunched, &attempts, func() (bool, error) { launches++; return !refuse, nil })
	deps := ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}

	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now))
	require.Equal(t, 1, launches)
	require.Zero(t, attempts, "a refused launch charges nothing")

	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now.Add(time.Minute)))
	require.Equal(t, 1, launches, "inside the backoff the pass does not ask again")

	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now.Add(3*time.Minute)))
	require.Equal(t, 2, launches, "past the backoff it tries again")

	refuse = false
	require.Equal(t, 1, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now.Add(10*time.Minute)))
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
	require.Equal(t, 1, attempts, "the launch that started is the one charged")
}

// An ordinary (non-stuck) needs-planning refusal is a ping-pong drive back
// into planning, not a standing escalation, and stays eligible for the
// durable pass exactly like any other planning failure — only the plan-stuck
// escalation itself (SC-2990) is left alone.
func TestReconcileFailedStages_OrdinaryPlanRefusalIsStillRelaunched(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt(PlanningStartedHeader, now.Add(-time.Hour)),
		cmt(markerBody(failureMarker(MarkerNeedsPlanning, needsPlanningReason)), now.Add(-10*time.Minute)),
	}}}
	var relaunched []BoardStage
	attempts := 0
	retry := recoveryRetry("", false, &relaunched, &attempts, nil)

	n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	require.Equal(t, 1, n)
	require.Equal(t, []BoardStage{BoardPlanning}, relaunched)
}

func TestReconcileFailedStages_BacksOffAfterAnErroredLaunch(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, 10*time.Minute)}
	var relaunched []BoardStage
	attempts, launches := 0, 0
	fail := true
	retry := recoveryRetry("", false, &relaunched, &attempts, func() (bool, error) {
		launches++
		if fail {
			return false, errors.New("container start failed")
		}
		return true, nil
	})
	deps := ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}

	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now))
	require.Equal(t, 1, launches)
	require.Zero(t, attempts, "an errored launch that started nothing charges no budget")

	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now.Add(time.Minute)))
	require.Equal(t, 1, launches, "inside the backoff the pass does not retry the error")

	fail = false
	require.Equal(t, 1, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now.Add(3*time.Minute)))
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
}

// Backoff doubles up to the cap and never below the floor.
func TestRecoveryBackoff_doublesToTheCap(t *testing.T) {
	b := newRecoveryBackoff(2*time.Minute, 5*time.Minute)
	now := time.Unix(0, 0)
	require.True(t, b.due("k", now))
	b.tried("k", now)
	require.False(t, b.due("k", now.Add(time.Minute)))
	require.True(t, b.due("k", now.Add(2*time.Minute)))
	b.tried("k", now)
	require.False(t, b.due("k", now.Add(3*time.Minute)))
	require.True(t, b.due("k", now.Add(4*time.Minute)))
	b.tried("k", now)
	require.True(t, b.due("k", now.Add(5*time.Minute)), "capped at five minutes")
	b.clear("k")
	require.True(t, b.due("k", now))
}

// The bound is observable exactly once: the first tick past it logs, later
// ticks do not, and the card is otherwise untouched.
func TestReconcileFailedStages_LogsTheBoundOnce(t *testing.T) {
	resetRecoveryBackoff(t)
	recoveryBoundWarned = sync.Map{}
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, FailedRecoveryBound+time.Hour)}
	var relaunched []BoardStage
	attempts := 0
	var buf bytes.Buffer
	deps := ReconcileDeps{LiveAgents: liveAgents(), Retry: recoveryRetry("", false, &relaunched, &attempts, nil), DaemonID: "d1", Logger: zerolog.New(&buf)}

	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now))
	require.Zero(t, reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), deps, now.Add(time.Hour)))

	require.Equal(t, 1, strings.Count(buf.String(), "past the recovery bound"))
	require.Empty(t, relaunched)
}
