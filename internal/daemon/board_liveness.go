package daemon

// Liveness verdicts a viewer can reach about the agent behind a board card.
// The empty string is deliberately not among them: it means UNKNOWN, and the
// distinction between "nothing is running" and "we could not ask" is the whole
// point of the set (SC-3569).
const (
	AgentLive      = "live"
	AgentDead      = "dead"
	AgentElsewhere = "elsewhere"
)

// AgentNamesForCard names every board agent that could legitimately be running
// the work a card currently shows. It is the viewer's half of the join the
// daemon already performs internally (board_reconcile.go:267-270), exported so
// the desktop looks for the SAME name the launcher used rather than
// re-implementing agentNameFor and drifting from it.
//
// A nil result means the card's current work is carried by NO named agent — a
// plain deploy runs in-process in the daemon (launchForwardStage hands
// BoardDoneStage to runDoneStage, which launches nothing), and a resting card
// runs nothing at all. The caller must then report liveness as unknown: silence
// from a stage that never had an agent is not evidence of death.
func AgentNamesForCard(card BoardViewCard) []string {
	stage := BoardStage(card.Stage)
	state := BoardState(card.State)
	switch {
	case stage == BoardDoneStage:
		// A done-stage card has an agent only while the pre-merge review→fix loop
		// or the deploy fixer is mid-flight; DeployPhase is the marker-derived
		// signal that the loop is running. Both halves and the deploy fixer are
		// listed because any of them legitimately owns the card between rounds.
		if card.DeployPhase == "" {
			return nil
		}
		return []string{
			agentNameFor(card.Key, prReviewAgentStage),
			agentNameFor(card.Key, prFixAgentStage),
			agentNameFor(card.Key, deployFixAgentStage),
		}
	case stage == BoardVerification && state == BoardDone && VerdictFailed(card.Verdict):
		// The rework a failed verdict triggers re-runs the IMPLEMENTATION stage
		// (isReworkTransition, board_transition.go:1684), so that is the agent
		// that owns the card while the board badges it "fixing…".
		return []string{agentNameFor(card.Key, BoardImplementation)}
	case state == BoardRunning || state == BoardQueued:
		// Only these three stages launch a named agent (launchForwardStage).
		if stage != BoardPlanning && stage != BoardImplementation && stage != BoardVerification {
			return nil
		}
		return []string{agentNameFor(card.Key, stage)}
	default:
		return nil
	}
}
