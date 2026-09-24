package daemon

import (
	"context"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// heldOn reports the ticket a thread's answered decision defers to: the
// waits-for of the newest [human:option-chosen] nothing has superseded. Empty
// for a thread that is not held — an ordinary answer, or a stage that has since
// started.
func heldOn(comments []tracker.Comment) string {
	_, choice, ok := optionChosenQueued(comments)
	if !ok {
		return ""
	}
	return waitsForOf(choice)
}

// refuseWaitCycle refuses a sequencing answer that nothing could ever clear: a
// wait on this ticket itself, or on a ticket that is already held on this one.
// Two tickets waiting for each other are each released only by the other
// finishing, and neither can finish, so the pair would sit held forever with
// the queued state's own rule — a HELD card is never stale by time — keeping
// every sweep off it (SC-5274). The blocker-link path refuses the same shape
// in cycleAmong; this is that guard for the answer path.
//
// A ticket whose thread cannot be read is treated as not part of a cycle, the
// way cycleAmong treats an unreadable blocker: the hold is the safe direction,
// and the board reports a pair it later finds (MarkWaitCycles).
func (d BoardTransitionDeps) refuseWaitCycle(ctx context.Context, pmKey, waitsFor, option string) error {
	if waitsFor == pmKey {
		return errors.WithDetails("this answer makes the ticket wait for itself, which nothing can clear",
			"pm", pmKey, "option", option)
	}
	theirs, err := d.Commenter.ListComments(ctx, waitsFor)
	if err != nil {
		d.Logger.Warn().Err(err).Str("pm", pmKey).Str("waits for", waitsFor).
			Msg("board decision: cannot read the ticket this answer waits for; recording the wait anyway")
		return nil
	}
	if heldOn(theirs) == pmKey {
		return errors.WithDetails(waitsFor+" already waits for "+pmKey+": this answer would make the two tickets wait for each other, so neither could ever start; start one of them instead",
			"pm", pmKey, "waitsFor", waitsFor, "option", option)
	}
	return nil
}

// waitCycleError is the card-face report of a pair recorded before the answer
// path refused it. It rides the card's Error so the held (paused) badge shows
// it, and names the way out because the hold has no timer to fall back on.
func waitCycleError(key, waitsFor string) string {
	if waitsFor == key {
		return "waits for itself, which nothing can clear — drop the card on its own column to start it"
	}
	return "waits for " + waitsFor + ", which waits for this ticket; neither can start on its own — drop either card on its own column to start it"
}

// MarkWaitCycles reports, on both cards, a pair of held cards that wait for
// each other. The pass that releases a held card asks only whether the ticket
// it waits for is closed, and neither of a pair ever will be, so without this
// the two sit "waiting for" one another indefinitely and the board shows
// nothing wrong (SC-5274). The map is the board's own card set: a partner
// outside it — another tracker, a closed ticket — is not a cycle this pass can
// see, and the release pass handles a closed one.
func MarkWaitCycles(cards map[string]BoardCard) {
	for key, card := range cards {
		if card.WaitsFor == "" {
			continue
		}
		other, ok := cards[card.WaitsFor]
		if !ok || other.WaitsFor != key {
			continue
		}
		card.Error = waitCycleError(key, card.WaitsFor)
		cards[key] = card
	}
}

// waitsForCycle names the drivable card a held card waits for when that card
// waits back, and "" when there is no such pair in the set.
func (dc DrivableCards) waitsForCycle(card ReconcileCard, waitsFor string) string {
	for _, other := range dc.cards {
		if other.Key == waitsFor && heldOn(other.Comments) == card.Key {
			return other.Key
		}
	}
	return ""
}
