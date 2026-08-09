package board

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/daemon"
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

func TestMarkAgentLiveness_graceBoundary(t *testing.T) {
	c := card("SC-1", string(daemon.BoardImplementation), string(daemon.BoardRunning), "d1", agentLaunchGrace+time.Second, livenessNow)
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
}

func TestMarkAgentLiveness_unparseableTimestampIsNeverDead(t *testing.T) {
	cards := []daemon.BoardViewCard{{
		Key: "SC-1", Stage: string(daemon.BoardImplementation), State: string(daemon.BoardRunning),
		StageDaemonID: "d1", StageEnteredAt: "not-a-time",
	}}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness)
}

func TestMarkAgentLiveness_plainDeployHasNoAgentToMiss(t *testing.T) {
	c := card("SC-1", string(daemon.BoardDoneStage), string(daemon.BoardRunning), "d1", 3*time.Hour, livenessNow)
	c.DeployPhase = ""
	cards := []daemon.BoardViewCard{c}
	MarkAgentLiveness(cards, LiveAgents{Names: map[string]bool{}, DaemonID: "d1", Now: livenessNow})
	assert.Empty(t, cards[0].AgentLiveness, "a plain deploy runs in-process; it never had an agent")
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
