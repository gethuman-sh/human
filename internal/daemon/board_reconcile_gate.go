package daemon

import (
	"github.com/gethuman-sh/human/internal/tracker"
)

// ProjectParticipation reports whether THIS machine opted into doing autonomous
// board work for the project a PM card belongs to. Whether a machine participates
// in a project's work is a decision its operator makes, not something that happens
// by default because the machine can see the tracker (SC-2047). A nil predicate
// means "participate in every visible project" — the backward-compatible default
// that keeps a single-daemon board unchanged; a configured machine narrows it.
type ProjectParticipation func(pmKey string) bool

// WorkGate is the single decision every work-driving reconcile pass consults to
// answer one question: may THIS machine drive this card forward? It is the
// by-construction choke point SC-2047 requires. Work-driving passes never receive
// a raw []ReconcileCard — only a DrivableCards built through this gate — so a path
// added later cannot act on work this machine cannot reach or does not own. The
// check is a property of the only collection a pass is given, not a call each path
// must remember and a future path might forget.
//
// The gate has two intents because ownership of a stage ends by FINISHING it:
//   - forTakeover governs reddening/relaunching a still-running stage. A running
//     stage is unfinished work bound to the machine that owns it; it is never
//     wrested away by another machine deciding from a distance that it stalled
//     (this is what retires the delay-only StuckRunningForeignGrace).
//   - forReview governs chaining a review or continuing a post-handoff loop. A
//     handoff is the owner's explicit signal that implementation finished, so any
//     participating machine that can genuinely obtain the branch may take it up.
type WorkGate struct {
	// reachable reports whether a card's branch resolves on THIS machine (local
	// ref or origin). nil disables the reachability arm — the "nil disables"
	// convention that keeps single-daemon boards unchanged.
	reachable BranchReachable
	// participates reports whether this machine opted into the card's project.
	// nil means participate in every visible project (backward-compatible default).
	participates ProjectParticipation
	// daemonID is this machine's identity, used to tell its OWN running stage from
	// a peer's when a card has no branch fact yet (the pre-branch ownership arm).
	daemonID string
}

// DrivableCards is the subset of the board a work-driving reconcile pass is
// permitted to act on. Work-driving passes accept ONLY this type; the raw
// []ReconcileCard is reserved for machine-local passes that act on this machine's
// own containers or on read-only forge state. The only way to obtain a
// DrivableCards is through WorkGate.forReview / WorkGate.forTakeover, whose sole
// constructors apply the gate — so a pass added later is handed cards that already
// passed the gate and cannot be handed cards that skipped it. This makes "no
// machine acts on work it cannot reach, for every path, by construction" a
// property the compiler enforces rather than a guard each path must remember.
type DrivableCards struct{ cards []ReconcileCard }

// forReview returns the cards this machine may chain a review for (or continue a
// post-handoff loop on): the project is one it participates in and the handoff
// branch is genuinely obtainable here. No ownership-by-identity arm — a handoff is
// the owner's explicit signal that implementation finished, so the stage is open
// to any participating machine that can reach it (SC-2047 obtainable handoffs).
func (g WorkGate) forReview(cards []ReconcileCard) DrivableCards {
	return DrivableCards{cards: filterCards(cards, g.drivableForReview)}
}

// forTakeover returns the cards this machine may red and relaunch: the project is
// one it participates in, the stage is not owned by a different machine, and its
// branch (if any) is reachable here. A stage stamped by a peer daemon is never
// takeable regardless of how long it has sat — ownership ends by finishing or by
// explicit release, never by a distant machine's timeout (SC-2047; this is what
// lets StuckRunningForeignGrace be retired rather than merely lengthened).
func (g WorkGate) forTakeover(cards []ReconcileCard) DrivableCards {
	return DrivableCards{cards: filterCards(cards, g.drivableForTakeover)}
}

func (g WorkGate) drivableForReview(card ReconcileCard) bool {
	if !g.participatesIn(card.Key) {
		return false
	}
	return g.reachableHere(g.derive(card))
}

func (g WorkGate) drivableForTakeover(card ReconcileCard) bool {
	if !g.participatesIn(card.Key) {
		return false
	}
	derived := g.derive(card)
	if !g.ownedHereOrUnowned(derived) {
		return false
	}
	return g.reachableHere(derived)
}

func (g WorkGate) derive(card ReconcileCard) BoardCard {
	return DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
}

func (g WorkGate) participatesIn(pmKey string) bool {
	return g.participates == nil || g.participates(pmKey)
}

// reachableHere reuses branchActionableHere so "can this machine act on the
// branch" is one fact defined in one place: a branch that resolves here, or no
// branch yet (nothing to obtain), or the gate disabled.
func (g WorkGate) reachableHere(derived BoardCard) bool {
	return branchActionableHere(derived, g.reachable)
}

// ownedHereOrUnowned reports whether a still-running stage is this machine's to
// take over: an unstamped stage (single-daemon or legacy) or one this daemon
// stamped itself. A stage stamped by a peer daemon is bound to that machine — the
// pre-branch counterpart of the reachability fact, so early stages with no branch
// yet are protected by identity rather than by a timer (SC-2047 ownership binding).
func (g WorkGate) ownedHereOrUnowned(derived BoardCard) bool {
	return derived.StageDaemonID == "" || derived.StageDaemonID == g.daemonID
}

func filterCards(cards []ReconcileCard, keep func(ReconcileCard) bool) []ReconcileCard {
	out := make([]ReconcileCard, 0, len(cards))
	for _, c := range cards {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}
