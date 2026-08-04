package board

import (
	"testing"

	"github.com/gethuman-sh/human/internal/daemon"
)

func TestMarkOwnership(t *testing.T) {
	cases := []struct {
		name        string
		card        daemon.BoardViewCard
		currentUser string
		wantNotMine bool
	}{
		{
			name:        "assigneeIsMe",
			card:        daemon.BoardViewCard{Assignee: "Alice"},
			currentUser: "Alice",
			wantNotMine: false,
		},
		{
			name:        "assigneeIsOther",
			card:        daemon.BoardViewCard{Assignee: "Bob"},
			currentUser: "Alice",
			wantNotMine: true,
		},
		{
			name:        "reporterFallbackIsMe",
			card:        daemon.BoardViewCard{Assignee: "", Reporter: "Alice"},
			currentUser: "Alice",
			wantNotMine: false,
		},
		{
			name:        "reporterFallbackIsOther",
			card:        daemon.BoardViewCard{Assignee: "", Reporter: "Bob"},
			currentUser: "Alice",
			wantNotMine: true,
		},
		{
			name:        "assigneeWinsOverReporter",
			card:        daemon.BoardViewCard{Assignee: "Alice", Reporter: "Bob"},
			currentUser: "Alice",
			wantNotMine: false,
		},
		{
			name:        "noOwner",
			card:        daemon.BoardViewCard{Assignee: "", Reporter: ""},
			currentUser: "Alice",
			wantNotMine: false,
		},
		{
			name:        "unknownViewer",
			card:        daemon.BoardViewCard{Assignee: "Bob"},
			currentUser: "",
			wantNotMine: false,
		},
		{
			name:        "trimsWhitespace",
			card:        daemon.BoardViewCard{Assignee: " Alice "},
			currentUser: "Alice",
			wantNotMine: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards := []daemon.BoardViewCard{tc.card}
			MarkOwnership(cards, tc.currentUser)
			if cards[0].NotMine != tc.wantNotMine {
				t.Fatalf("MarkOwnership(%+v, %q): NotMine = %v, want %v",
					tc.card, tc.currentUser, cards[0].NotMine, tc.wantNotMine)
			}
		})
	}
}
