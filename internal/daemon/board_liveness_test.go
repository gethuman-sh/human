package daemon

import (
	"slices"
	"testing"
)

// livenessCard builds the minimum BoardViewCard AgentNamesForCard reads, so a
// case states only the fields that steer the join and a later field addition
// does not silently change what these tests assert.
func livenessCard(stage BoardStage, state BoardState) BoardViewCard {
	return BoardViewCard{Key: "SC-1", Stage: string(stage), State: string(state)}
}

func assertNames(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("agent names = %v, want %v", got, want)
	}
}

// The three stages launchForwardStage actually launches a named agent for are
// the three that can be joined against a running container; the name must be
// the launcher's own, which is why AgentNamesForCard calls agentNameFor rather
// than formatting the string itself.
func TestAgentNamesForCard_runningStages(t *testing.T) {
	for _, stage := range []BoardStage{BoardPlanning, BoardImplementation, BoardVerification} {
		t.Run(string(stage), func(t *testing.T) {
			got := AgentNamesForCard(livenessCard(stage, BoardRunning))
			assertNames(t, got, []string{"board-SC-1-" + string(stage)})
		})
	}
}

// A queued card has already been claimed by a launch, so it is joined the same
// way as a running one — otherwise the launch race would read as "no agent".
func TestAgentNamesForCard_queuedStageStillNamesItsAgent(t *testing.T) {
	got := AgentNamesForCard(livenessCard(BoardImplementation, BoardQueued))
	assertNames(t, got, []string{"board-SC-1-implementation"})
}

// A plain deploy runs in-process in the daemon with no agent at all, so there is
// no name to miss. nil means unknown, never dead — asserting this is the guard
// against every deploying card reddening.
func TestAgentNamesForCard_plainDeployHasNone(t *testing.T) {
	card := livenessCard(BoardDoneStage, BoardRunning)
	card.DeployPhase = ""
	assertNames(t, AgentNamesForCard(card), nil)
}

// Both loop halves are named regardless of which half the marker says started:
// the halves hand off between rounds, so the card is legitimately owned by
// either at the moment the viewer looks.
func TestAgentNamesForCard_prLoopHalves(t *testing.T) {
	for _, phase := range []string{DeployPhasePRReview, DeployPhasePRFix} {
		t.Run(phase, func(t *testing.T) {
			card := livenessCard(BoardDoneStage, BoardRunning)
			card.DeployPhase = phase
			assertNames(t, AgentNamesForCard(card), []string{
				"board-SC-1-prreview",
				"board-SC-1-prfix",
			})
		})
	}
}

// The deploy fixer is deliberately NOT named here. DeployPhase only ever names a
// PR-loop half, so this branch is unreachable while deploy-fix is the current
// work; naming it would only ever match a container left over from an earlier
// round and report a false "live". Pinned as a test because the omission looks
// like an oversight to the next reader (SC-3569).
func TestAgentNamesForCard_omitsTheDeployFixer(t *testing.T) {
	card := livenessCard(BoardDoneStage, BoardRunning)
	card.DeployPhase = DeployPhasePRReview
	if slices.Contains(AgentNamesForCard(card), "board-SC-1-deployfix") {
		t.Fatal("deployfix must not be joined: DeployPhase never names it, so a match could only be a stale container from an earlier round")
	}
}

// The rework a failed verdict triggers re-runs the implementation stage, so the
// card the board badges "fixing…" is owned by the build agent, not by a
// verification one that has already exited.
func TestAgentNamesForCard_failedVerdictIsTheBuildAgent(t *testing.T) {
	card := livenessCard(BoardVerification, BoardDone)
	card.Verdict = "fail"
	assertNames(t, AgentNamesForCard(card), []string{"board-SC-1-implementation"})
}

// A card that is not running anything has no agent to find; silence from it is
// not evidence of death.
func TestAgentNamesForCard_restingCard(t *testing.T) {
	assertNames(t, AgentNamesForCard(livenessCard(BoardBacklog, BoardDone)), nil)
}

// A stage that launches no named agent must return nil even while running,
// rather than a name that can never match and would therefore read as dead.
func TestAgentNamesForCard_runningStageWithoutAnAgent(t *testing.T) {
	assertNames(t, AgentNamesForCard(livenessCard(BoardBacklog, BoardRunning)), nil)
}
