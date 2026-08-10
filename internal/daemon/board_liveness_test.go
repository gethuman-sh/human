package daemon

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestAgentNamesForCard_FailedCardStillNamesItsAgent covers SC-4151 A1: a
// failure marker is durable and a run need not have ended when one lands, so a
// red card's agent must still be looked for. Before this a failed card returned
// nil and its liveness was never even asked.
func TestAgentNamesForCard_FailedCardStillNamesItsAgent(t *testing.T) {
	for _, stage := range []BoardStage{BoardPlanning, BoardImplementation, BoardVerification} {
		got := AgentNamesForCard(livenessCard(stage, BoardFailed))
		assert.Equal(t, []string{agentNameFor("SC-1", stage)}, got, string(stage))
	}
}

func TestAgentNamesForCard_FailedLoopCardNamesBothHalves(t *testing.T) {
	got := AgentNamesForCard(BoardViewCard{
		Key: "SC-3852", Stage: string(BoardDoneStage), State: string(BoardFailed),
		DeployPhase: DeployPhasePRReview,
	})
	assert.Equal(t, []string{agentNameFor("SC-3852", prReviewAgentStage), agentNameFor("SC-3852", prFixAgentStage)}, got)
}

// A failed done-stage card with no loop half behind it — the deploy path reddened
// it — stays unknown rather than being answered with a PR-loop container from an
// earlier round.
func TestAgentNamesForCard_FailedDeployCardNamesNothing(t *testing.T) {
	got := AgentNamesForCard(livenessCard(BoardDoneStage, BoardFailed))
	assert.Nil(t, got)
}

// A failed BACKLOG card launches no agent, so nothing is named for it.
func TestAgentNamesForCard_FailedBacklogCardNamesNothing(t *testing.T) {
	got := AgentNamesForCard(livenessCard(BoardBacklog, BoardFailed))
	assert.Nil(t, got)
}

// SC-4406: a failed card whose ticket still shows a start in ANOTHER stage
// names that stage's agent too. The measured case is SC-3853 — a red
// done-stage card sending a person to check a pull request while the
// implementation agent nobody asked about was alive and working.
func TestAgentNamesForCard_FailedCardAlsoNamesTheOtherRunningStage(t *testing.T) {
	card := livenessCard(BoardDoneStage, BoardFailed)
	card.DeployPhase = DeployPhasePRReview
	card.RunningStage = string(BoardImplementation)
	assertNames(t, AgentNamesForCard(card), []string{
		"board-SC-1-prreview",
		"board-SC-1-prfix",
		"board-SC-1-implementation",
	})
}

// A failed done-stage card the deploy path reddened has no loop half, so the
// other running stage is the ONLY name — the case that used to answer nil and
// leave liveness unknown.
func TestAgentNamesForCard_FailedDeployCardNamesTheOtherRunningStage(t *testing.T) {
	card := livenessCard(BoardDoneStage, BoardFailed)
	card.RunningStage = string(BoardImplementation)
	assertNames(t, AgentNamesForCard(card), []string{"board-SC-1-implementation"})
}

// The widening is failed-only on purpose. For a RUNNING card the question is
// whether THIS stage's agent is alive, and a container lingering in a
// neighbouring stage would answer it falsely — so the field is ignored.
func TestAgentNamesForCard_RunningCardIgnoresTheOtherStage(t *testing.T) {
	card := livenessCard(BoardVerification, BoardRunning)
	card.RunningStage = string(BoardImplementation)
	assertNames(t, AgentNamesForCard(card), []string{"board-SC-1-verification"})
}
