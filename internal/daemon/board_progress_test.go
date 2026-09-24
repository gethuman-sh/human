package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var progressNow = time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

func progressCard(key string) BoardViewCard {
	return BoardViewCard{Key: key, Stage: string(BoardImplementation), State: string(BoardRunning)}
}

func probeOf(m map[string]AgentProgress) AgentProgressProbe {
	return func(name string) (AgentProgress, bool) {
		p, ok := m[name]
		return p, ok
	}
}

// SC-5328: silence within the short budget is a working agent, not a hang.
func TestMarkAgentProgress_silenceWithinBudgetIsNotStalled(t *testing.T) {
	cards := []BoardViewCard{progressCard("SC-1")}
	MarkAgentProgress(cards, probeOf(map[string]AgentProgress{
		"board-SC-1-implementation": {LastEventAt: progressNow.Add(-2 * time.Minute), ModelRequest: ModelRequestNone},
	}), "d1", progressNow)
	require.NotNil(t, cards[0].AgentProgress)
	assert.False(t, cards[0].AgentProgress.Stalled)
	assert.Equal(t, 120, cards[0].AgentProgress.IdleSeconds)
	assert.Equal(t, int(IdleGrace.Seconds()), cards[0].AgentProgress.BudgetSeconds)
	assert.Empty(t, cards[0].AgentProgress.Outstanding)
	assert.Equal(t, "d1", cards[0].AgentProgress.DaemonID)
	assert.Equal(t, "board-SC-1-implementation", cards[0].AgentProgress.Agent)
}

// SC-5328: a genuine hang — silent past the short budget with nothing
// outstanding — is reported stalled, with the silence and the budget it broke.
func TestMarkAgentProgress_hangBeyondBudgetIsStalled(t *testing.T) {
	cards := []BoardViewCard{progressCard("SC-1")}
	MarkAgentProgress(cards, probeOf(map[string]AgentProgress{
		"board-SC-1-implementation": {LastEventAt: progressNow.Add(-4 * time.Minute), ModelRequest: ModelRequestNone},
	}), "d1", progressNow)
	require.NotNil(t, cards[0].AgentProgress)
	assert.True(t, cards[0].AgentProgress.Stalled)
	assert.Equal(t, 240, cards[0].AgentProgress.IdleSeconds)
	assert.Equal(t, 180, cards[0].AgentProgress.BudgetSeconds)
}

// SC-5328: a run waiting on a dispatched subagent holds the generous budget,
// so twenty silent minutes are still work in flight — and the card can say so.
func TestMarkAgentProgress_subagentWaitKeepsTheGenerousBudget(t *testing.T) {
	cards := []BoardViewCard{progressCard("SC-1")}
	MarkAgentProgress(cards, probeOf(map[string]AgentProgress{
		"board-SC-1-implementation": {LastEventAt: progressNow.Add(-20 * time.Minute), ModelRequest: ModelRequestNone, Subagents: 1},
	}), "d1", progressNow)
	require.NotNil(t, cards[0].AgentProgress)
	assert.False(t, cards[0].AgentProgress.Stalled)
	assert.Equal(t, int(WorkingIdleGrace.Seconds()), cards[0].AgentProgress.BudgetSeconds)
	assert.Equal(t, "a subagent", cards[0].AgentProgress.Outstanding)
}

// SC-5328: an unknown model state buys the generous budget (SC-3853) and is
// named as such, and a blocked agent is neither stalled nor working.
func TestMarkAgentProgress_unknownModelStateAndBlocked(t *testing.T) {
	cards := []BoardViewCard{progressCard("SC-1"), progressCard("SC-2")}
	MarkAgentProgress(cards, probeOf(map[string]AgentProgress{
		"board-SC-1-implementation": {LastEventAt: progressNow.Add(-10 * time.Minute)},
		"board-SC-2-implementation": {LastEventAt: progressNow.Add(-10 * time.Minute), ModelRequest: ModelRequestNone, Blocked: true},
	}), "d1", progressNow)
	require.NotNil(t, cards[0].AgentProgress)
	assert.False(t, cards[0].AgentProgress.Stalled)
	assert.Equal(t, "an unknown model state", cards[0].AgentProgress.Outstanding)
	require.NotNil(t, cards[1].AgentProgress)
	assert.False(t, cards[1].AgentProgress.Stalled)
	assert.True(t, cards[1].AgentProgress.Blocked)
}

// SC-5328: an agent the daemon has never heard from, a card with no named
// agent, and a nil probe all leave the judgement absent — never a verdict.
func TestMarkAgentProgress_absentEvidenceLeavesNoJudgement(t *testing.T) {
	cards := []BoardViewCard{progressCard("SC-1"), {Key: "SC-2", Stage: string(BoardDoneStage), State: string(BoardRunning)}}
	MarkAgentProgress(cards, probeOf(map[string]AgentProgress{}), "d1", progressNow)
	assert.Nil(t, cards[0].AgentProgress)
	assert.Nil(t, cards[1].AgentProgress)
	MarkAgentProgress(cards, nil, "d1", progressNow)
	assert.Nil(t, cards[0].AgentProgress)
}
