package daemon

// Liveness verdicts a viewer can reach about the agent behind a board card.
// The empty string is deliberately not among them: it means UNKNOWN, and the
// distinction between "nothing is running" and "we could not ask" is the whole
// point of the set (SC-3569).
const (
	AgentLive = "live"
	AgentDead = "dead"
	// AgentRecovering is "dead, but the machine still owes it a try": a running
	// planning/implementation/verification card whose agent is gone, but not
	// yet past StuckRunningGrace — the window reconcileStuckRunning itself
	// waits out before relaunching (board_reconcile.go:482). Rendering this the
	// same as AgentDead would ask a person to retry work the daemon's own
	// bounded relaunch has not yet had its turn to fix (the SC-1830 rule
	// board.agentLaunchGrace already applies to the queued class); this value
	// lets the viewer paint the machine register instead until that turn has
	// passed (SC-3569 PR review finding).
	AgentRecovering = "recovering"
	AgentElsewhere  = "elsewhere"
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
		// is mid-flight; DeployPhase is the marker-derived signal that it is. Both
		// halves are listed because either legitimately owns the card between
		// rounds, and which one is running says nothing about whether the other's
		// container has yet been reaped.
		//
		// A FAILED done-stage card reports a phase too, naming the half that was
		// last started underneath the failure (deployPhaseFor →
		// doneStageStartedHalf). That is the SC-3852 case: the loop reds a step
		// whose container goes on working, and without a phase a red card's agent
		// was never even looked for (SC-4151 A1).
		//
		// The deploy FIXER is deliberately absent from both, and its liveness
		// stays unknown: DeployPhase only ever names a PR-loop half, so a card
		// whose done stage last saw a deploy or deploy-fix launch reports no
		// phase and returns nil here. Naming the deployfix agent would therefore
		// only ever be consulted while deploy-fix is NOT the current work —
		// turning a not-yet-reaped container from an earlier round into a false
		// "live". Covering it needs a wire signal the viewer does not have;
		// teaching the loop-half derivation the deploy-fix header instead would
		// silently widen doneStageLoopActive, which gates the PR-loop re-drive
		// and the stuck-running guard.
		if card.DeployPhase == "" {
			return nil
		}
		return []string{
			agentNameFor(card.Key, prReviewAgentStage),
			agentNameFor(card.Key, prFixAgentStage),
		}
	case stage == BoardVerification && state == BoardDone && VerdictFailed(card.Verdict):
		// The rework a failed verdict triggers re-runs the IMPLEMENTATION stage
		// (isReworkTransition, board_transition.go:1684), so that is the agent
		// that owns the card while the board badges it "fixing…".
		return []string{agentNameFor(card.Key, BoardImplementation)}
	case state == BoardRunning || state == BoardQueued || state == BoardFailed:
		// Only these three stages launch a named agent (launchForwardStage).
		//
		// BoardFailed is here for the opposite question to the one this function
		// was written for. A failure marker is durable and a run is not required
		// to have ended when one lands, so a card can be red while the agent it
		// describes is still working — measured on SC-3852, where a
		// pr-review-failed marker stood for an hour over a reviewer that went on
		// to record changes-requested (SC-4151 A1). Asking who would be running
		// this stage is the same question whether the last marker said started or
		// failed; only the answer's use differs.
		if stage != BoardPlanning && stage != BoardImplementation && stage != BoardVerification {
			return nil
		}
		return []string{agentNameFor(card.Key, stage)}
	default:
		return nil
	}
}
