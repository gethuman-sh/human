package daemon

import (
	"sort"

	"github.com/gethuman-sh/human/internal/tracker"
)

// Placement is a card's (stage, state) — the pair the board renders into a
// column and a badge, and the pair every reconcile pass judges before it acts.
//
// It exists because the two halves were carried as loose strings that any code
// could set independently. The derivation computed a pair by one set of rules
// and later passes assigned over it (`card.State = BoardFailed`), so the result
// was whatever the last writer said, and nothing anywhere asked whether the
// combination was one the machine could actually produce. The legal pairs are
// declared in pipeline-fsm.json and were checked only by a conformance test —
// after the fact, on a document, not on the value.
//
// So the pair moves as one value with named transitions. Every way a card's
// placement changes during derivation is a method here; a placement is only
// reachable by the rule that names it, and legal() says whether the machine can
// name the result at all.
type Placement struct {
	stage BoardStage
	state BoardState
}

// Stage and State expose the halves for reading. Writing goes through the
// transitions.
func (p Placement) Stage() BoardStage { return p.stage }
func (p Placement) State() BoardState { return p.state }

// atStage places a card at the head of a stage with no state — the idle card
// waiting for its turn. The backlog card and the ideas card are this.
func atStage(stage BoardStage) Placement {
	return Placement{stage: stage, state: BoardIdle}
}

// fromMarker is the ordinary placement: a marker classified into a (stage,
// state) pair. ok is false for a body that is not a board marker, which is the
// same answer ClassifyMarker gives.
func fromMarker(body string) (Placement, bool) {
	stage, state, ok := ClassifyMarker(body)
	if !ok {
		return Placement{}, false
	}
	return Placement{stage: stage, state: state}, true
}

// inStage moves a placement to the state the stage's own latest marker decides.
// This is the second pass of derivation: the furthest stage is settled, and the
// marker newest within it says whether that stage is running, done or failed.
func (p Placement) inStage(state BoardState) Placement {
	return Placement{stage: p.stage, state: state}
}

// supersededBy hands the card to a strictly-newer marker anywhere on the
// ticket. A stale red the pipeline already moved past, or a done-stage loop a
// chosen rebuild restarted from an earlier stage: the card follows the ticket's
// current activity rather than a terminal the machine has left behind.
func (p Placement) supersededBy(newer Placement) Placement { return newer }

// queuedAt records that a decision ([human:option-chosen]) has re-queued a
// stage whose relaunched agent has not posted its started marker yet. It is the
// one placement no marker produces — synthesized here rather than classified —
// so it exists only as the result of this transition.
func queuedAt(stage BoardStage) Placement {
	return Placement{stage: stage, state: BoardQueued}
}

// pausedOnDecision turns a running OR failed card into a waiting-on-human card
// while an open decision block names its own stage or an earlier one the answer
// would rework: the card is not working, and it is not dead either — it is
// waiting for a human. Server-side twin of the client's decision-badge branch.
// The caller applies the same at-or-before stage-rank predicate as the failure
// watcher and reconcile pass (stagePausedOnOptions), so a block naming a stage
// the card has not yet reached — a stale or target-relaunch block — never
// clears an active run (SC-1669).
//
// Accepting BoardFailed alongside BoardRunning is the residual safety net
// (SC-1957): a card can reach this point already reddened by a *-failed marker
// posted before the recovery machinery's own relaunch consumed the question
// (openOptionsBlock only treats a later BoardRunning marker or an option-chosen
// as consumption — a *-failed marker does not). Surfacing that combination as
// waiting-on-human rather than a plain failure is exactly what makes an
// otherwise-erased question visible. This also subsumes the former SC-1857
// done-stage PR-loop escalation special case: the escalation's block names the
// implementation stage while the card's furthest stage is done, so the general
// at-or-before rank rule (rank 3 <= rank 5) pauses it the same way the former
// bespoke escalation check used to.
//
// Every other state is returned unchanged.
func (p Placement) pausedOnDecision() Placement {
	if p.state != BoardRunning && p.state != BoardFailed {
		return p
	}
	return Placement{stage: p.stage, state: BoardIdle}
}

// determinedBy hands the card to a terminal determination — the last word about
// the whole ticket (already merged, no fix warranted, a gate's stop, a launch
// refused for want of a plan). Each files under a stage that ranks BELOW the
// phantom runs it supersedes, so it must override furthest-stage-wins rather
// than lose to it.
func (p Placement) determinedBy(terminal Placement) Placement { return terminal }

// failedOn reds a card in place, keeping its stage. It is how a malformed
// decision block already on a ticket surfaces: the card says what is wrong
// where the decision would have been, rather than the block vanishing.
func (p Placement) failedOn() Placement {
	return Placement{stage: p.stage, state: BoardFailed}
}

// legalPlacements is every (stage, state) pair the machine can produce: one per
// classified marker, plus the placements derivation synthesizes without a
// marker to classify.
//
// Built from orderedMarkerSpecs rather than restated, so a marker added there
// is legal here the moment it exists — a second list would be wrong the first
// time a marker is added, and wrong silently.
func legalPlacements() map[Placement]bool {
	legal := map[Placement]bool{
		// No pipeline activity yet, and the two placements that come from the
		// ticket itself rather than from any marker: a closed ticket is hidden,
		// an idea-labelled one sits in Ideas until promotion removes the label.
		atStage(BoardBacklog): true,
		atStage(BoardHidden):  true,
		atStage(BoardIdeas):   true,
	}
	for _, spec := range orderedMarkerSpecs {
		legal[Placement{stage: spec.Stage, state: spec.State}] = true
		// Any stage a marker can reach can also be paused on an open decision
		// and re-queued by an answered one. Both are synthesized in derivation,
		// not classified from a marker, so neither appears in the spec table.
		legal[Placement{stage: spec.Stage, state: BoardIdle}] = true
		legal[Placement{stage: spec.Stage, state: BoardQueued}] = true
		// A malformed decision block reds the card in whatever stage it sits.
		legal[Placement{stage: spec.Stage, state: BoardFailed}] = true
	}
	return legal
}

// legal reports whether the machine has a way to reach this placement. A card
// placed where nothing can reach is a rendering the board cannot explain and no
// reconcile pass will move — the shape of every stuck-card bug this type exists
// to make unwritable.
func (p Placement) legal() bool { return legalPlacements()[p] }

// String renders a placement the way this project already writes one down —
// "planning/running", and "backlog/" for an idle card. The trailing slash is
// kept deliberately: it is the form the recorded placement baseline uses, and a
// prettier rendering would be a second spelling of the same fact.
func (p Placement) String() string {
	return string(p.stage) + "/" + string(p.state)
}

// sortedLegalPlacements lists every legal placement in a stable order, so a
// test reporting the set reads the same on every run.
func sortedLegalPlacements() []Placement {
	all := make([]Placement, 0, len(legalPlacements()))
	for p := range legalPlacements() {
		all = append(all, p)
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].stage != all[b].stage {
			return stageRank[all[a].stage] < stageRank[all[b].stage]
		}
		return all[a].state < all[b].state
	})
	return all
}

// placeAt is the ONLY writer of a card's stage and state. The two fields stay
// exported because they are the board's wire format, but everything that
// decides where a card goes goes through here — which is what keeps a later
// pass from assigning over a placement the derivation reasoned its way to.
func (c *BoardCard) placeAt(p Placement) {
	c.Stage = p.stage
	c.State = p.state
}

// cardAt is the only constructor of a placed card: every BoardCard the
// derivation returns starts here, so no card is ever built with a stage and a
// state written independently of each other.
func cardAt(p Placement) BoardCard {
	var card BoardCard
	card.placeAt(p)
	return card
}

// placementOf names a pair someone else already decided — a card that arrived
// over the wire, a state the FSM document describes. It is deliberately NOT how
// production code reaches a placement: everything that DECIDES where a card goes
// uses the transitions above, so a pair only exists because a rule produced it.
func placementOf(stage BoardStage, state BoardState) Placement {
	return Placement{stage: stage, state: state}
}

// placement reads a card's current placement back as one value.
func (c BoardCard) placement() Placement {
	return placementOf(c.Stage, c.State)
}

// markerPlacement is the placement a comment's marker classifies to, together
// with the comment itself — the pair the derivation carries while it decides
// which marker owns the card.
type markerPlacement struct {
	placement Placement
	comment   tracker.Comment
}
