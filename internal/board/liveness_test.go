package board

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// card builds a BoardViewCard fixture for the liveness tests. enteredAgo is
// how long before now the stage marker landed.
func card(key, stage, state, daemonID string, enteredAgo time.Duration, now time.Time) daemon.BoardViewCard {
	return daemon.BoardViewCard{
		Key: key, Stage: stage, State: state, StageDaemonID: daemonID,
		StageEnteredAt: now.Add(-enteredAgo).Format(time.RFC3339),
	}
}

var livenessNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func TestMarkAgentLiveness_liveAgentFound(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{
		Names: map[string]bool{"board-SC-1-implementation": true}, DaemonID: "d1", Now: livenessNow,
	})
	assert.Equal(t, daemon.AgentLive, cards[0].AgentLiveness)
}

func TestMarkAgentLiveness_deadWhenOursAndAbsent(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness)
}

func TestMarkAgentLiveness_elsewhereWhenForeignDaemon(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d2", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentElsewhere, cards[0].AgentLiveness)
}

func TestMarkAgentLiveness_unknownWhenMarkerUnsigned(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness)
}

func TestMarkAgentLiveness_unknownWhenLocalDaemonIDMissing(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness)
}

func TestMarkAgentLiveness_unknownWhenDiscoveryCouldNotRun(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: nil, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness, "a broken engine must never condemn a card")
}

func TestMarkAgentLiveness_freshLaunchIsNotDead(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 30*time.Second, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness)
}

// Just past agentLaunchGrace, reconcileStuckRunning's own StuckRunningGrace
// relaunch is not yet due for a running implementation card (the class it
// owns), so this must read as machine-owed recovery, not a person's turn.
func TestMarkAgentLiveness_graceBoundary(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", agentLaunchGrace+time.Second, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentRecovering, cards[0].AgentLiveness)
}

// A running planning/implementation/verification card is exactly the class
// reconcileStuckRunning owns (board_reconcile.go:379-410): it relaunches at
// StuckRunningGrace (15m), measured from the same StageEnteredAt this overlay
// reads. Between agentLaunchGrace and StuckRunningGrace the card must render
// as machine-owed recovery — never AgentDead, which the board paints in the
// person-facing register with a "Retry it" ask. Once StuckRunningGrace has
// actually passed, the machine's own pass has already had its turn, so the
// verdict must become AgentDead — silence past that point IS the person's
// turn, and the ticket's core value (surfacing a truly abandoned card) must
// not be lost to an unbounded machine register.
func TestMarkAgentLiveness_recoveringUntilStuckRunningGraceThenDead(t *testing.T) {
	for _, stage := range []daemon.BoardStage{daemon.BoardPlanning, daemon.BoardImplementation, daemon.BoardVerification} {
		recovering := card("SC-1", string(stage), string(daemon.BoardRunning), "d1", daemon.StuckRunningGrace-time.Second, livenessNow)
		recoveringCards := []daemon.BoardViewCard{recovering}
		MarkAgentLiveness(recoveringCards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
		assert.Equal(t, daemon.AgentRecovering, recoveringCards[0].AgentLiveness,
			"%s: reconcileStuckRunning's own relaunch is still due — must not read as needing a person yet", stage)

		dead := card("SC-1", string(stage), string(daemon.BoardRunning), "d1", daemon.StuckRunningGrace+time.Second, livenessNow)
		deadCards := []daemon.BoardViewCard{dead}
		MarkAgentLiveness(deadCards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
		assert.Equal(t, daemon.AgentDead, deadCards[0].AgentLiveness,
			"%s: past StuckRunningGrace the machine's own pass has already had its turn — now it is the person's", stage)
	}
}

// loopCard builds a done/running PR-loop card: the stage marker landed
// enteredAgo before now, and the named half's agent stopped stoppedAgo before
// now. A zero stoppedAgo means no stop was recorded at all.
func loopCard(phase string, enteredAgo time.Duration) daemon.BoardViewCard {
	c := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", enteredAgo, livenessNow)
	c.DeployPhase = phase
	return c
}

// SC-5091: the gap between a PR reviewer's exit and the loop's next re-drive
// is the machine's turn, not a person's. reconcilePRLoops runs only inside
// reconcileOnce, on BoardReconcileInterval jittered by ±BoardReconcileJitter —
// one to three minutes, not instantly — and the card's only other clock,
// StageEnteredAt, is the pr-review-started marker's time, long past
// agentLaunchGrace by the time a real review ends. So the window is measured
// from the recorded stop, and inside it the card must read AgentRecovering:
// every ticket of the 2026-09-22 campaign showed the amber "agent not running
// — Retry it" ask over a reviewer that had simply finished.
func TestMarkAgentLiveness_doneStageLoopRecoversUntilTheRedriveIsDue(t *testing.T) {
	for _, phase := range []string{daemon.DeployPhasePRReview, daemon.DeployPhasePRFix} {
		c := loopCard(phase, 20*time.Minute)
		cards := []daemon.BoardViewCard{c}
		MarkAgentLiveness(cards, LiveAgents{
			Names:    map[string]bool{},
			DaemonID: "d1",
			Now:      livenessNow,
			StoppedAt: map[string]time.Time{
				"board-SC-1-prreview": livenessNow.Add(-30 * time.Second),
				"board-SC-1-prfix":    livenessNow.Add(-30 * time.Second),
			},
		})
		assert.Equal(t, daemon.AgentRecovering, cards[0].AgentLiveness,
			"%s: the loop's own re-drive is still due — the card must not ask a person to retry", phase)
	}
}

// The window still ends: a loop card whose half stopped longer ago than the
// re-drive can possibly take has not been re-driven, and that IS a person's
// turn. Without this the machine register would be unbounded and a dead
// daemon's card would stay calm forever.
func TestMarkAgentLiveness_doneStageLoopIsDeadPastTheRedriveWindow(t *testing.T) {
	c := loopCard(daemon.DeployPhasePRReview, 30*time.Minute)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{
		Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow,
		StoppedAt: map[string]time.Time{
			"board-SC-1-prreview": livenessNow.Add(-(prLoopRedriveGrace() + time.Second)),
		},
	})
	assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness)
}

// Absence of a stop record is not a claim that the agent is alive: with no
// record, and with a record that predates the stage (an earlier round's half,
// already adjudicated), the card reads exactly as it did before this existed.
func TestMarkAgentLiveness_doneStageLoopWithoutAStopRecordIsUnchanged(t *testing.T) {
	cases := map[string]map[string]time.Time{
		"never asked":      nil,
		"asked, nothing":   {},
		"another agent":    {"board-SC-1-implementation": livenessNow.Add(-30 * time.Second)},
		"before the stage": {"board-SC-1-prreview": livenessNow.Add(-25 * time.Minute)},
	}
	for name, stopped := range cases {
		c := loopCard(daemon.DeployPhasePRReview, 20*time.Minute)
		cards := []daemon.BoardViewCard{c}
		MarkAgentLiveness(cards, LiveAgents{
			Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow, StoppedAt: stopped,
		})
		assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness, name)
	}
}

// A stop record must never soften the three agent stages: their recovery is
// reconcileStuckRunning's, measured from StageEnteredAt, and SC-1542's
// headline class (verification/done/verdict=failed) is recovered by no pass at
// all. Both must read exactly as before with StoppedAt populated.
func TestMarkAgentLiveness_stopRecordDoesNotWidenTheOtherClasses(t *testing.T) {
	stopped := map[string]time.Time{
		"board-SC-1-implementation": livenessNow.Add(-30 * time.Second),
	}
	past := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", daemon.StuckRunningGrace+time.Second, livenessNow)
	cards := []daemon.BoardViewCard{past}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow, StoppedAt: stopped})
	assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness,
		"past StuckRunningGrace an implementation card is the person's turn, stop record or not")

	failed := card("SC-1", string(daemon.BoardVerification), string(daemon.BoardDone), "d1", 10*time.Minute, livenessNow)
	failed.Verdict = "fail"
	failedCards := []daemon.BoardViewCard{failed}
	MarkAgentLiveness(failedCards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow, StoppedAt: stopped})
	assert.Equal(t, daemon.AgentDead, failedCards[0].AgentLiveness, "SC-1542's class must not soften")
}

// The board must not ask a person to retry a loop card at the same instant the
// daemon's own re-drive starts one — the SC-1830 rule agentLaunchGrace already
// carries against QueuedLaunchGrace. reconcilePRLoops runs inside reconcileOnce
// on a jittered BoardReconcileInterval, so the grace must outlast the worst
// tick that interval can produce.
func TestPRLoopRedriveGrace_outlastsTheWorstReconcileTick(t *testing.T) {
	worst := time.Duration(float64(daemon.BoardReconcileInterval) * (1 + daemon.BoardReconcileJitter))
	assert.Greater(t, prLoopRedriveGrace(), worst,
		"a loop card must never read as needing a person while the loop's own re-drive is still due")
}

// The board must not ask a person to retry a card the daemon is still due to
// relaunch on its own: reconcileQueuedLaunch recovers a queued card after
// QueuedLaunchGrace, and a dead verdict paints the amber --turn-person badge.
// Equal windows put the two exactly in step, so the grace has to outlast it.
func TestAgentLaunchGrace_outlastsTheDaemonsOwnRecovery(t *testing.T) {
	assert.Greater(t, agentLaunchGrace, daemon.QueuedLaunchGrace,
		"a card must never read as needing a person while the machine's own relaunch pass is still due")
	// Pin the StuckRunningGrace relation too, so the two constants can never
	// drift back into step the way agentLaunchGrace and QueuedLaunchGrace once
	// did: agentLaunchGrace is the point a running card first reads as
	// machine-owed recovery, and it must land strictly before the daemon's own
	// StuckRunningGrace relaunch — otherwise a running card would jump straight
	// from "starting" to AgentDead with no recovery window at all.
	assert.Less(t, agentLaunchGrace, daemon.StuckRunningGrace,
		"a running card must have a machine-owed recovery window before StuckRunningGrace, not a straight jump to AgentDead")
}

// The relation above is only worth pinning for what it renders, so pin that
// too: a queued card measured from the very timestamp reconcileQueuedLaunch
// measures (both read the option-chosen comment's Created time) must still not
// read dead at the moment that pass becomes due, nor while it is running.
func TestMarkAgentLiveness_queuedCardIsNotDeadWhileTheRelaunchIsDue(t *testing.T) {
	for _, ago := range []time.Duration{daemon.QueuedLaunchGrace, daemon.QueuedLaunchGrace + 30*time.Second} {
		c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardQueued), "d1", ago, livenessNow)
		cards := []daemon.BoardViewCard{c}
		MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
		assert.Empty(t, cards[0].AgentLiveness,
			"at %s the daemon's own relaunch is due or under way — the card must not send the reader to retry it", ago)
	}
}

func TestMarkAgentLiveness_unparseableTimestampIsNeverDead(t *testing.T) {
	cards := []daemon.BoardViewCard{{
		Key: "SC-1", Stage: string(daemon.BoardImplementation), State: string(daemon.BoardRunning),
		StageDaemonID: "d1", StageEnteredAt: "not-a-time",
	}}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness)
}

// The overlay's counterpart to SC-4150: a deploy queued behind another for hours
// must not read dead here either. It cannot, and this pins why — a done-stage
// card names an agent only when DeployPhase is a PR-loop half, and none of the
// three deploy entry routes leaves one set: [human:deploy-started] and
// [human:deploy-fix-started] are not loop halves, and [human:pr-review-passed]
// retires the phase (board_state.go, deployPhaseFor).
// SC-5878: none of the deploy entry routes leaves one set — and a fourth value
// exists that names no half either: a deploy queued behind this ticket's own
// container.
func TestMarkAgentLiveness_plainDeployHasNoAgentToMiss(t *testing.T) {
	c := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	c.DeployPhase = ""
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness, "a plain deploy runs in-process; it never had an agent")

	// The same property for a queued-behind-another deploy: an approved review's
	// merge names no loop half, so it too must carry no agent to miss.
	derived := daemon.DeriveBoardCard([]tracker.Comment{
		{Body: daemon.PRReviewStartedHeader, Created: livenessNow.Add(-3 * time.Hour)},
		{Body: daemon.PRReviewPassedHeader, Created: livenessNow.Add(-3*time.Hour + time.Minute)},
	}, tracker.CategoryUnstarted, false)
	require.Empty(t, derived.DeployPhase, "an approved review's merge names no loop half")

	c2 := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	c2.DeployPhase = derived.DeployPhase
	cards2 := []daemon.BoardViewCard{c2}
	MarkAgentLiveness(cards2, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards2[0].AgentLiveness, "an approve-then-merge deploy runs in-process too; it never had an agent")

	c3 := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	c3.DeployPhase = daemon.DeployPhaseQueued
	cards3 := []daemon.BoardViewCard{c3}
	MarkAgentLiveness(cards3, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards3[0].AgentLiveness, "a queued deploy waits in the daemon; it never had an agent")
}

func TestMarkAgentLiveness_prLoopJoinsEitherHalf(t *testing.T) {
	c := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	c.DeployPhase = "pr-fix"

	cardsFix := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cardsFix, LiveAgents{
		Names: map[string]bool{"board-SC-1-prfix": true}, DaemonID: "d1", Now: livenessNow,
	})
	assert.Equal(t, daemon.AgentLive, cardsFix[0].AgentLiveness)

	cardsReview := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cardsReview, LiveAgents{
		Names: map[string]bool{"board-SC-1-prreview": true}, DaemonID: "d1", Now: livenessNow,
	})
	assert.Equal(t, daemon.AgentLive, cardsReview[0].AgentLiveness)
}

func TestMarkAgentLiveness_failedVerdictJoinsTheReworkBuild(t *testing.T) {
	c := card("SC-1", string(daemon.BoardVerification), string(daemon.BoardDone), "d1", 3*time.Hour, livenessNow)
	c.Verdict = "fail"

	live := []daemon.BoardViewCard{c}
	MarkAgentLiveness(live, LiveAgents{
		Names: map[string]bool{"board-SC-1-implementation": true}, DaemonID: "d1", Now: livenessNow,
	})
	assert.Equal(t, daemon.AgentLive, live[0].AgentLiveness)

	dead := []daemon.BoardViewCard{c}
	MarkAgentLiveness(dead, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentDead, dead[0].AgentLiveness)
}

// A verification/done/verdict=failed card is never BoardRunning (it derives
// BoardDone), so stuckRunningCandidate (board_reconcile.go:379-380) never
// selects it and no daemon reconcile pass ever recovers it. machineOwesATry
// must therefore stay false for this class at every instant inside the
// agentLaunchGrace..StuckRunningGrace window, not merely outside it — this is
// the ticket's headline SC-1542 case, and the window is exactly where a
// running implementation card legitimately reads AgentRecovering, so it is
// exactly where this class must NOT borrow that reading.
func TestMarkAgentLiveness_failedVerdictStaysDeadThroughoutTheRecoveryWindow(t *testing.T) {
	for _, ago := range []time.Duration{
		agentLaunchGrace + time.Second,
		10 * time.Minute,
		daemon.StuckRunningGrace - time.Second,
	} {
		c := card("SC-1", string(daemon.BoardVerification), string(daemon.BoardDone), "d1", ago, livenessNow)
		c.Verdict = "fail"
		cards := []daemon.BoardViewCard{c}
		MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
		assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness,
			"at %s: no reconcile pass ever recovers this class, so it must read as the person's turn, never machine-owed recovery", ago)
	}
}

func TestMarkAgentLiveness_restingCardStaysUnknown(t *testing.T) {
	c := card("SC-1", string(daemon.BoardBacklog), string(daemon.BoardDone), "d1", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness)
}

func TestAgentNamesFromContainers(t *testing.T) {
	names := AgentNamesFromContainers([]string{
		"human-agent-board-SC-1-implementation",
		"some-other-container",
		"human-agent-",
		"  human-agent-board-SC-2-prfix  ",
	})
	assert.Equal(t, map[string]bool{
		"board-SC-1-implementation": true,
		"board-SC-2-prfix":          true,
	}, names)
}

func TestMarkAgentLiveness_sanitizedKeyMatchesLauncherName(t *testing.T) {
	c := card("SC/1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{
		Names: map[string]bool{"board-SC-1-implementation": true}, DaemonID: "d1", Now: livenessNow,
	})
	assert.Equal(t, daemon.AgentLive, cards[0].AgentLiveness, "the join must use agentNameFor's sanitize, not raw concatenation")
}

// SC-4406, the whole point end to end: a red card whose ticket has a live agent
// in ANOTHER stage reads live, so the badge renders the machine register instead
// of sending a person to intervene on work that is still being done.
func TestMarkAgentLiveness_FailedCardIsLiveWhenAnotherStageRuns(t *testing.T) {
	c := card("SC-3853", string(daemon.BoardDoneStage), string(daemon.BoardFailed), "d1", 3*time.Hour, livenessNow)
	c.DeployPhase = daemon.DeployPhasePRReview
	c.RunningStage = string(daemon.BoardImplementation)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{
		Names: map[string]bool{"board-SC-3853-implementation": true}, DaemonID: "d1", Now: livenessNow,
	})
	assert.Equal(t, daemon.AgentLive, cards[0].AgentLiveness)
}

// The softening rests on a RUNNING CONTAINER, never on the marker that named the
// stage. A stale started marker with nothing behind it leaves the card dead —
// which is what keeps a genuine failure from being hidden forever by a lie the
// stuck-running pass has not caught up with yet.
func TestMarkAgentLiveness_FailedCardStaysDeadWhenTheOtherStageIsAMarkerOnly(t *testing.T) {
	c := card("SC-3853", string(daemon.BoardDoneStage), string(daemon.BoardFailed), "d1", 3*time.Hour, livenessNow)
	c.DeployPhase = daemon.DeployPhasePRReview
	c.RunningStage = string(daemon.BoardImplementation)
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness)
}

// SC-5328: a present agent the daemon judges hung reads stalled, not live —
// but only on the daemon's own word about THIS agent on THIS machine. A
// judgement about another machine's agent, about a different agent name, or
// no judgement at all leaves the agent live exactly as it rendered before.
func TestMarkAgentLiveness_stalledOnTheDaemonsOwnJudgement(t *testing.T) {
	live := LiveAgents{Names: map[string]bool{"board-SC-1-implementation": true}, DaemonID: "d1", Now: livenessNow}
	mk := func(p *daemon.BoardAgentProgress) daemon.BoardViewCard {
		c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
		c.AgentProgress = p
		return c
	}
	cards := []daemon.BoardViewCard{
		mk(&daemon.BoardAgentProgress{Agent: "board-SC-1-implementation", DaemonID: "d1", Stalled: true, IdleSeconds: 240, BudgetSeconds: 180}),
		mk(&daemon.BoardAgentProgress{Agent: "board-SC-1-implementation", DaemonID: "d1", Stalled: false, IdleSeconds: 60, BudgetSeconds: 180}),
		mk(&daemon.BoardAgentProgress{Agent: "board-SC-1-implementation", DaemonID: "d2", Stalled: true}),
		mk(&daemon.BoardAgentProgress{Agent: "board-SC-1-planning", DaemonID: "d1", Stalled: true}),
		mk(nil),
	}
	MarkAgentLiveness(cards, live)
	assert.Equal(t, daemon.AgentStalled, cards[0].AgentLiveness, "hung on the daemon's own judgement")
	assert.Equal(t, daemon.AgentLive, cards[1].AgentLiveness, "silent within budget is working")
	assert.Equal(t, daemon.AgentLive, cards[2].AgentLiveness, "another machine's judgement is not about this container")
	assert.Equal(t, daemon.AgentLive, cards[3].AgentLiveness, "a judgement about a different agent")
	assert.Equal(t, daemon.AgentLive, cards[4].AgentLiveness, "no judgement renders as before")
}

// SC-5328: a stalled judgement without a container to apply it to says
// nothing — the absent-agent verdicts are unchanged by it.
func TestMarkAgentLiveness_stalledJudgementNeedsAPresentAgent(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	c.AgentProgress = &daemon.BoardAgentProgress{Agent: "board-SC-1-implementation", DaemonID: "d1", Stalled: true}
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness)
}
