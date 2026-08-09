package board

import (
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// MarkOwnership dims every card that is not the viewer's own. Ownership is the
// assignee, falling back to the reporter when there is no assignee (SC-3339) —
// which decides nearly every card, since the pipeline sets no assignee.
//
// Full opacity means "mine", so everything else dims, INCLUDING a card whose
// owner could not be resolved at all (SC-3404). An unowned card is not mine
// either, and rendering it like my own work is the reading that misleads —
// previously it was left bright, so a card with no recorded owner was
// indistinguishable from one I filed myself.
//
// The viewer is a SET of names, not one: the same person is a display name on
// Shortcut and a login on GitHub, so any declared identity matching the owner
// means the card is yours.
//
// An UNKNOWN VIEWER is the one case that still dims nothing, and it is not the
// same question as an unknown owner: with no identity to compare against every
// card would dim, which is a board carrying no distinction at all rather than a
// board telling you nothing here is yours.
//
// Viewer-local by construction: called from the desktop overlay (applyLocal),
// never from Compose, so the shared board stays identical for every consumer.
func MarkOwnership(cards []daemon.BoardViewCard, viewer vieweridentity.Identity) {
	if !viewer.Known() {
		return
	}
	for i := range cards {
		cards[i].NotMine = !viewer.Matches(ownerOf(cards[i]))
	}
}

// ownerOf reads the shared rule off the card. The rule itself lives in
// daemon.OwnerOf because the daemon's work gate decides what to WORK by the same
// answer this decides what to DIM; if they disagreed the board would dim cards
// the machine is driving, or show as yours cards it will never touch.
func ownerOf(c daemon.BoardViewCard) string {
	return daemon.OwnerOf(c.Assignee, c.Reporter)
}
