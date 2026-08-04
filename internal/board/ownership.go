package board

import (
	"strings"

	"github.com/gethuman-sh/human/internal/daemon"
)

// MarkOwnership dims every card whose owner is provably someone other than the
// current viewer. Ownership is the assignee, falling back to the reporter when
// there is no assignee (SC-3339) — which decides nearly every card, since the
// pipeline sets no assignee. A card is dimmed (NotMine=true) ONLY when the
// viewer's name is known AND the card has an owner AND they differ; a card with
// no owner, or an unknown viewer identity, is left untouched so it renders at
// full opacity. Dimming is a hint applied on certainty, never on a guess.
//
// Viewer-local by construction: called from the desktop overlay (applyLocal),
// never from Compose, so the shared board stays identical for every consumer.
func MarkOwnership(cards []daemon.BoardViewCard, currentUser string) {
	me := strings.TrimSpace(currentUser)
	if me == "" {
		return
	}
	for i := range cards {
		owner := ownerOf(cards[i])
		if owner == "" {
			continue
		}
		cards[i].NotMine = owner != me
	}
}

// ownerOf is the assignee, or the reporter when there is no assignee.
func ownerOf(c daemon.BoardViewCard) string {
	if a := strings.TrimSpace(c.Assignee); a != "" {
		return a
	}
	return strings.TrimSpace(c.Reporter)
}
