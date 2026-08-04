package board

import (
	"testing"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

func TestMarkOwnership(t *testing.T) {
	cases := []struct {
		name        string
		card        daemon.BoardViewCard
		viewer      []string
		wantNotMine bool
	}{
		{
			name:        "assigneeIsMe",
			card:        daemon.BoardViewCard{Assignee: "Alice"},
			viewer:      []string{"Alice"},
			wantNotMine: false,
		},
		{
			name:        "assigneeIsOther",
			card:        daemon.BoardViewCard{Assignee: "Bob"},
			viewer:      []string{"Alice"},
			wantNotMine: true,
		},
		{
			name:        "reporterFallbackIsMe",
			card:        daemon.BoardViewCard{Assignee: "", Reporter: "Alice"},
			viewer:      []string{"Alice"},
			wantNotMine: false,
		},
		{
			name:        "reporterFallbackIsOther",
			card:        daemon.BoardViewCard{Assignee: "", Reporter: "Bob"},
			viewer:      []string{"Alice"},
			wantNotMine: true,
		},
		{
			name:        "assigneeWinsOverReporter",
			card:        daemon.BoardViewCard{Assignee: "Alice", Reporter: "Bob"},
			viewer:      []string{"Alice"},
			wantNotMine: false,
		},
		{
			name:        "noOwner",
			card:        daemon.BoardViewCard{Assignee: "", Reporter: ""},
			viewer:      []string{"Alice"},
			wantNotMine: false,
		},
		{
			name:        "unknownViewer",
			card:        daemon.BoardViewCard{Assignee: "Bob"},
			viewer:      nil,
			wantNotMine: false,
		},
		{
			// One person, two trackers: the Shortcut display name and the
			// GitHub login are both "me", so a card owned by either is mine.
			name:        "secondDeclaredIdentityMatches",
			card:        daemon.BoardViewCard{Assignee: "alice-gh"},
			viewer:      []string{"Alice", "alice-gh"},
			wantNotMine: false,
		},
		{
			// A tracker that cases a login differently must not turn a card of
			// mine into someone else's.
			name:        "caseInsensitive",
			card:        daemon.BoardViewCard{Assignee: "ALICE-GH"},
			viewer:      []string{"alice-gh"},
			wantNotMine: false,
		},
		{
			name:        "trimsWhitespace",
			card:        daemon.BoardViewCard{Assignee: " Alice "},
			viewer:      []string{"Alice"},
			wantNotMine: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards := []daemon.BoardViewCard{tc.card}
			MarkOwnership(cards, vieweridentity.Identity{Names: tc.viewer})
			if cards[0].NotMine != tc.wantNotMine {
				t.Fatalf("MarkOwnership(%+v, %v): NotMine = %v, want %v",
					tc.card, tc.viewer, cards[0].NotMine, tc.wantNotMine)
			}
		})
	}
}
