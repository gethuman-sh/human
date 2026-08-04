package board

import (
	"strings"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// MarkOwnership dims every card whose owner is provably someone other than the
// current viewer. Ownership is the assignee, falling back to the reporter when
// there is no assignee (SC-3339) — which decides nearly every card, since the
// pipeline sets no assignee. A card is dimmed (NotMine=true) ONLY when the
// viewer is known AND the card has an owner AND they differ; a card with no
// owner, or an unknown viewer, is left untouched so it renders at full opacity.
// Dimming is a hint applied on certainty, never on a guess.
//
// The viewer is a SET of names, not one: the same person is a display name on
// Shortcut and a login on GitHub, so any declared identity matching the owner
// means the card is yours.
//
// Viewer-local by construction: called from the desktop overlay (applyLocal),
// never from Compose, so the shared board stays identical for every consumer.
func MarkOwnership(cards []daemon.BoardViewCard, viewer vieweridentity.Identity) {
	if !viewer.Known() {
		return
	}
	for i := range cards {
		owner := ownerOf(cards[i])
		if owner == "" {
			continue
		}
		cards[i].NotMine = !viewer.Matches(owner)
	}
}

// ownerOf is the assignee, or the reporter when there is no assignee.
func ownerOf(c daemon.BoardViewCard) string {
	if a := strings.TrimSpace(c.Assignee); a != "" {
		return a
	}
	return strings.TrimSpace(c.Reporter)
}
