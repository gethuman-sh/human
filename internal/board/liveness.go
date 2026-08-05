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
const agentLaunchGrace = 2 * time.Minute

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
// The rule is three-valued plus unknown because on a board several daemons drive,
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
	if withinGrace(card.StageEnteredAt, live.Now) {
		return ""
	}
	return daemon.AgentDead
}

// withinGrace reports whether the card's stage marker is too fresh for a missing
// agent to mean anything yet. An absent or unparseable timestamp counts as
// fresh: a death verdict must never rest on a time we could not read.
func withinGrace(stageEnteredAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, stageEnteredAt)
	if err != nil {
		return true
	}
	return now.Sub(t) < agentLaunchGrace
}
