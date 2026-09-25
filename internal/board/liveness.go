package board

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/daemon"
)

// agentLaunchGrace bounds how long after a stage marker lands a missing agent
// still reads as "starting" rather than "dead". A *-started marker is posted
// BEFORE launchAgent returns (board_transition.go:620-624), and a devcontainer
// can take tens of seconds to come up, so without this window every launch
// would flash a false "agent not running" on the card that just started.
//
// It is derived from the daemon's own recovery window rather than set to a
// number of its own, because the machine must get its turn before the person
// is asked for theirs. A dead verdict paints the amber --turn-person register
// and says "Retry the stage" (livenessBadge, board-queue.ts); reaching that
// while reconcileQueuedLaunch is still due to relaunch the card by itself
// demands a person for work the machine is already fixing — the SC-1830 rule
// badgeInfo states in its own comment. Two independent constants that merely
// happened to be equal put the two exactly in step, so the board asked for a
// retry at the same instant the daemon started one.
const agentLaunchGrace = daemon.QueuedLaunchGrace + time.Minute

// prLoopRedriveGrace bounds how long after a PR-loop half's recorded stop the
// card still reads as machine-owed recovery rather than as a person's turn.
//
// Derived from the pass that actually re-drives the loop, never a number of its
// own, for the reason agentLaunchGrace states: reconcilePRLoops runs only
// inside reconcileOnce (board_reconcile.go:233), on BoardReconcileInterval
// perturbed by up to ±BoardReconcileJitter, so the latest a re-drive can land
// after an exit is interval×(1+jitter). The extra minute is the same margin
// agentLaunchGrace carries, so the board can never ask for a retry in the
// instant the daemon starts one. A function rather than a const because both
// inputs are vars the daemon's own tests shorten.
func prLoopRedriveGrace() time.Duration {
	worst := daemon.BoardReconcileInterval
	if daemon.BoardReconcileJitter > 0 {
		worst = time.Duration(float64(daemon.BoardReconcileInterval) * (1 + daemon.BoardReconcileJitter))
	}
	return worst + time.Minute
}

// LiveAgents is what ONE machine can see about running board agents at the
// moment the overlay is applied.
type LiveAgents struct {
	// Names is the set of board agent names running on THIS machine. A NIL map
	// means the question could not be asked at all — no Docker engine here, a
	// container listing that errored, or the quick first-paint path — which is
	// emphatically NOT the same as an empty map ("asked, and nothing is
	// running"). Nil leaves every card unknown, per the LiveAgentLister
	// precedent that a liveness that cannot be established is never acted on.
	Names map[string]bool
	// StoppedAt is when THIS machine recorded each board agent as stopped, by
	// agent name (agent.StoppedBoardAgents). It is the only clock that says
	// when an agent ENDED: StageEnteredAt is the start marker's time, which for
	// a mid-flight PR review is tens of minutes earlier and cannot distinguish
	// a reviewer that just exited from one that never ran. A nil or missing
	// entry is not a claim that the agent is alive — it leaves the card on the
	// verdict it had before this existed (SC-5091).
	StoppedAt map[string]time.Time
	// DaemonID is this host's daemon id — the value this machine signs onto the
	// markers it posts (DaemonInfo.DaemonID). Empty when it could not be read,
	// which also leaves liveness unknown: without it, a missing agent cannot be
	// attributed to this machine rather than to a peer.
	DaemonID string
	// Now is injected so the launch-grace window is testable.
	Now time.Time
}

// AgentNamesFromContainers reduces a container listing to the set of board agent
// names running on this machine. Agent containers are named
// ContainerPrefix+<agent name> (agent/manager.go:94), so the prefix is stripped
// to recover the name the launcher and the daemon's own reconcile passes use.
// Anything else running on the engine is not a board agent and is ignored.
func AgentNamesFromContainers(containerNames []string) map[string]bool {
	names := make(map[string]bool, len(containerNames))
	for _, c := range containerNames {
		if n, ok := strings.CutPrefix(strings.TrimSpace(c), agent.ContainerPrefix); ok && n != "" {
			names[n] = true
		}
	}
	return names
}

// MarkAgentLiveness overlays what this machine can see about running agents onto
// each card, so a card can say whether an agent is alive behind it instead of
// asserting a spinner from a tracker comment alone (SC-3569).
//
// Viewer-local by construction, exactly like MarkOwnership: called from the
// desktop overlay (applyLocal), never from Compose, so the shared board stays
// identical for every consumer.
//
// The rule is five-valued plus unknown because on a board several daemons drive,
// "no agent on this machine" is only ever evidence about THIS machine.
func MarkAgentLiveness(cards []daemon.BoardViewCard, live LiveAgents) {
	if live.Names == nil {
		// The question could not be asked. Leaving every card unknown renders the
		// board exactly as it did before this existed, which is the only safe
		// reading of silence.
		return
	}
	for i := range cards {
		cards[i].AgentLiveness = livenessOf(cards[i], live)
	}
}

// livenessOf decides one card's verdict.
func livenessOf(card daemon.BoardViewCard, live LiveAgents) string {
	names := daemon.AgentNamesForCard(card)
	if len(names) == 0 {
		// Nothing named could be running this card (a plain deploy runs in-process
		// in the daemon; a resting card runs nothing), so there is nothing to
		// conclude from finding no agent.
		return ""
	}
	for _, n := range names {
		if live.Names[n] {
			if stalledHere(card, n, live) {
				return daemon.AgentStalled
			}
			return daemon.AgentLive
		}
	}
	// From here on the card has no agent HERE. That only becomes a statement
	// about the work once we know whose stage it is.
	if card.StageDaemonID == "" || live.DaemonID == "" {
		// An unsigned marker (an older daemon, a hand-written comment) or an
		// unknown local id leaves ownership open — the DaemonBusy precedent:
		// absence of a signal is never treated as proof.
		return ""
	}
	if card.StageDaemonID != live.DaemonID {
		return daemon.AgentElsewhere
	}
	if withinGrace(card.StageEnteredAt, live.Now, agentLaunchGrace) {
		return ""
	}
	if machineOwesATry(card, names, live) {
		// The agent is gone, but the daemon's own recovery for this card's
		// class has not yet had its turn. Saying AgentDead here paints the
		// amber --turn-person register and asks for a retry of work the
		// machine is about to do by itself.
		return daemon.AgentRecovering
	}
	return daemon.AgentDead
}

// stalledHere reports whether the daemon's own progress judgement says the
// agent found running is hung. Three joins guard it: the judgement must be
// about the agent that was found (not a reaped run's namesake), made by this
// machine's daemon (progress is daemon-local), and say stalled. Absent or
// foreign judgements leave the agent live, as it rendered before (SC-5328).
func stalledHere(card daemon.BoardViewCard, agent string, live LiveAgents) bool {
	p := card.AgentProgress
	return p != nil && p.Agent == agent && p.DaemonID == live.DaemonID && p.Stalled
}

// machineOwesATry reports whether the daemon's own recovery for this card is
// still due, so a missing agent here is the machine's turn rather than the
// person's. Two classes, and they do NOT share a clock:
//
//   - A running planning/implementation/verification card is the class
//     reconcileStuckRunning owns (board_reconcile.go:379). That pass measures
//     StuckRunningGrace from the same StageEnteredAt this overlay reads, so the
//     marker's own timestamp answers it.
//   - A running done-stage card is a PR review<->fix loop, re-driven by
//     reconcilePRLoops instead. That pass takes no grace of its own, but "no
//     grace" is not "immediately": it runs only on the reconcile tick, one to
//     three minutes apart, and reading it as instant is what put every card of
//     a campaign run into the amber "agent not running — Retry it" register
//     for the minutes after its reviewer simply finished (SC-5091). Its clock
//     cannot be StageEnteredAt — that is when the review STARTED, long past
//     agentLaunchGrace by the time a real review ends — so it is the agent's
//     recorded stop, and absent that record the card is left on AgentDead
//     exactly as before.
//
// A verification/done/verdict=failed card is never BoardRunning (it derives
// BoardDone), so no pass recovers it and it stays on the dead path unchanged —
// the SC-1542 case, which must not soften.
func machineOwesATry(card daemon.BoardViewCard, names []string, live LiveAgents) bool {
	if card.State != string(daemon.BoardRunning) {
		return false
	}
	switch daemon.BoardStage(card.Stage) {
	case daemon.BoardPlanning, daemon.BoardImplementation, daemon.BoardVerification:
		return withinGrace(card.StageEnteredAt, live.Now, daemon.StuckRunningGrace)
	case daemon.BoardDoneStage:
		return withinPRLoopRedrive(card, names, live)
	default:
		return false
	}
}

// withinPRLoopRedrive reports whether a done-stage loop card's newest recorded
// stop is recent enough that reconcilePRLoops has not yet had its turn.
//
// The stop must postdate the stage to be ABOUT this stage — the guard
// recordedDeath applies for the same reason (board_reconcile.go:494): a half
// that stopped before the current started marker belongs to an earlier round,
// already adjudicated, and reading it here would hold a genuinely abandoned
// card in the machine register indefinitely.
func withinPRLoopRedrive(card daemon.BoardViewCard, names []string, live LiveAgents) bool {
	if card.DeployPhase != daemon.DeployPhasePRReview && card.DeployPhase != daemon.DeployPhasePRFix {
		return false
	}
	entered, err := time.Parse(time.RFC3339, card.StageEnteredAt)
	if err != nil {
		return false
	}
	stopped := newestStopAfter(names, live.StoppedAt, entered)
	if stopped.IsZero() {
		return false
	}
	return live.Now.Sub(stopped) < prLoopRedriveGrace()
}

// newestStopAfter returns the latest recorded stop among names that postdates
// entered, or the zero time when none does. Either loop half legitimately owns
// the card between rounds, so the newest of the two is the one that ended the
// work the card is showing.
func newestStopAfter(names []string, stopped map[string]time.Time, entered time.Time) time.Time {
	var newest time.Time
	for _, n := range names {
		at, ok := stopped[n]
		if !ok || at.IsZero() || !at.After(entered) {
			continue
		}
		if at.After(newest) {
			newest = at
		}
	}
	return newest
}

// withinGrace reports whether the card's stage marker is too fresh, against
// the given window, for a missing agent to mean anything yet. An absent or
// unparseable timestamp counts as fresh: a death verdict must never rest on a
// time we could not read.
func withinGrace(stageEnteredAt string, now time.Time, grace time.Duration) bool {
	t, err := time.Parse(time.RFC3339, stageEnteredAt)
	if err != nil {
		return true
	}
	return now.Sub(t) < grace
}
