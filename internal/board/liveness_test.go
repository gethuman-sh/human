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

// reconcilePRLoops re-drives a done-stage PR review<->fix loop card with NO
// grace at all (board_reconcile.go:253), unlike the three running stages
// above. A done-stage loop card must therefore move straight to AgentDead the
// moment agentLaunchGrace passes — StuckRunningGrace has no bearing on this
// class, and it must not linger in the machine register longer than before.
func TestMarkAgentLiveness_doneStageLoopStaysDeadNotRecovering(t *testing.T) {
	c := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", agentLaunchGrace+time.Second, livenessNow)
	c.DeployPhase = "pr-review"
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Equal(t, daemon.AgentDead, cards[0].AgentLiveness)
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
// selects it and no daemon reconcile pass ever recovers it. recoverableByStuckRunning
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
