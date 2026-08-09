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

// OwnerIdentity is the set of names that mean "this machine's person". Declared
// as an interface here so the gate does not depend on how identity is
// configured; internal/vieweridentity.Identity satisfies it, which is the same
// value the board's dimming overlay compares against — the machine refusing to
// work a card and the board dimming it are then one decision, not two.
type OwnerIdentity interface {
	// Known reports whether any identity is declared at all.
	Known() bool
	// Matches reports whether name is one of this machine's identities. An empty
	// name never matches.
	Matches(name string) bool
}

// TicketIdentity resolves the identity that governs a PM key's project. It is a
// function of the key rather than one value because a daemon can serve several
// registered projects and each declares its own `me:` — resolving once globally
// would apply one project's identity to another's tickets. A nil function, or a
// nil return, disables the ownership arm for that card ("nil disables", the
// package convention that keeps an unconfigured install working exactly as before).
type TicketIdentity func(pmKey string) OwnerIdentity

// WorkGate is the single decision every work-driving reconcile pass consults to
// answer one question: may THIS machine drive this card forward? It is the
// by-construction choke point SC-2047 requires. Work-driving passes never receive
// a raw []ReconcileCard — only a DrivableCards built through this gate — so a path
// added later cannot act on work this machine cannot reach or does not own. The
// check is a property of the only collection a pass is given, not a call each path
// must remember and a future path might forget.
//
// Whose TICKET it is comes first and applies to every intent: a machine does
// autonomous work only on tickets its own person owns (SC-4063). That is a
// property of the ticket, not of what the pass wants to do with it, so it is not
// an arm of one intent — it is checked before the intent is consulted at all.
// A human gesture never reaches here and is deliberately not restricted by it.
//
// The gate then has three intents because ownership of a STAGE ends by FINISHING it:
//   - forTakeover governs reddening/relaunching a still-running stage. A running
//     stage is unfinished work bound to the machine that owns it; it is never
//     wrested away by another machine deciding from a distance that it stalled
//     (this is what retires the delay-only StuckRunningForeignGrace).
//   - forReview governs chaining a review or continuing a post-handoff loop. A
//     handoff is the owner's explicit signal that implementation finished, so any
//     participating machine that can genuinely obtain the branch may take it up.
//   - forOwnWork governs a pass that needs no branch at all. It applies ownership
//     and participation and stops there. reconcileShippedFailures is the case:
//     it asks the forge whether the PR merged, and the branch it would probe is
//     DELETED at merge — gating it on reachability would filter out precisely the
//     cards it exists to clear.
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
	// identityFor resolves the person whose tickets this machine may work, per
	// project. nil disables the arm — an install that declares no `me:` keeps
	// today's behaviour rather than stopping work on upgrade.
	identityFor TicketIdentity
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

// forOwnWork returns the cards this machine may act on with no branch involved:
// the ticket is its person's and the project is one it participates in. Used by
// the pass whose subject is the forge's merge state rather than a branch.
func (g WorkGate) forOwnWork(cards []ReconcileCard) DrivableCards {
	return DrivableCards{cards: filterCards(cards, g.drivableForOwnWork)}
}

func (g WorkGate) drivableForOwnWork(card ReconcileCard) bool {
	return g.mineToWork(card) && g.participatesIn(card.Key)
}

func (g WorkGate) drivableForReview(card ReconcileCard) bool {
	if !g.drivableForOwnWork(card) {
		return false
	}
	return g.reachableHere(g.derive(card))
}

func (g WorkGate) drivableForTakeover(card ReconcileCard) bool {
	if !g.drivableForOwnWork(card) {
		return false
	}
	derived := g.derive(card)
	if !g.ownedHereOrUnowned(derived) {
		return false
	}
	return g.reachableHere(derived)
}

// mineToWork reports whether the TICKET belongs to this machine's person — the
// assignee, or the reporter when unassigned (daemon.OwnerOf, the same rule the
// board dims by).
//
// Two distinct absences are both treated as "carry on", and deliberately:
//
//   - No identity declared. An install with no `me:` section has never expressed
//     whose work this machine does, and refusing everything would stop a working
//     single-daemon board dead on upgrade.
//   - No owner resolvable on the card. An empty owner is "unknown", not "someone
//     else's": a tracker that fails to resolve a member name hands back an empty
//     string with the error swallowed, so reading absence as refusal would let one
//     flaky members call stand the whole pipeline down for a tick.
//
// Only a RESOLVED owner that is demonstrably not this machine's person refuses.
func (g WorkGate) mineToWork(card ReconcileCard) bool {
	if g.identityFor == nil {
		return true
	}
	identity := g.identityFor(card.Key)
	if identity == nil || !identity.Known() {
		return true
	}
	owner := OwnerOf(card.Assignee, card.Reporter)
	if owner == "" {
		return true
	}
	return identity.Matches(owner)
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
