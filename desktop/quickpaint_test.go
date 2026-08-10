//go:build wailsapp

package main

import (
	"testing"

	"github.com/gethuman-sh/human/internal/board"
	"github.com/gethuman-sh/human/internal/boardprefs"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// liteResults is what the quick fetch returns: issues with no derived
// BoardCards, so nothing tells the composer which column a card belongs in.
func liteResults(keys ...string) []daemon.TrackerIssuesResult {
	issues := make([]tracker.Issue, 0, len(keys))
	for _, key := range keys {
		issues = append(issues, tracker.Issue{Key: key, Title: key, Status: "In Progress"})
	}
	return []daemon.TrackerIssuesResult{{
		TrackerName: "human",
		TrackerKind: "shortcut",
		TrackerRole: "pm",
		Project:     "board",
		Issues:      issues,
	}}
}

// The order CardsQuick composes in is load-bearing, and this is the case that
// proves it: the viewer overlay reads a card's stage to assign its Ideas
// sub-column, so a card restored into Ideas AFTER applyLocal would land there
// without one. Restoring first gives it both (SC-4324).
func TestQuickPaintRestoresColumnsBeforeTheViewerOverlay(t *testing.T) {
	last := daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", Stage: string(daemon.BoardIdeas)},
		{Key: "SC-2", Stage: string(daemon.BoardVerification)},
	}}

	view := board.RestoreStages(board.Compose(liteResults("SC-1", "SC-2"), true), last)
	got := applyLocal(view, map[string]int{"SC-1": 2}, nil, boardprefs.Prefs{}, vieweridentity.Identity{}, 0, board.LiveAgents{})

	byKey := map[string]daemon.BoardViewCard{}
	for _, card := range got.Cards {
		byKey[card.Key] = card
	}
	if stage := byKey["SC-1"].Stage; stage != string(daemon.BoardIdeas) {
		t.Fatalf("SC-1 stage = %q, want %q", stage, daemon.BoardIdeas)
	}
	if col := byKey["SC-1"].IdeaColumn; col != 2 {
		t.Fatalf("SC-1 idea sub-column = %d, want 2 — the overlay ran before the card was placed", col)
	}
	if stage := byKey["SC-2"].Stage; stage != string(daemon.BoardVerification) {
		t.Fatalf("SC-2 stage = %q, want %q", stage, daemon.BoardVerification)
	}
}

// Without a remembered board the quick paint is exactly what it was: every open
// ticket in Backlog, which is the behaviour a daemon predating the route keeps.
func TestQuickPaintWithoutARememberedBoardIsUnchanged(t *testing.T) {
	view := board.RestoreStages(board.Compose(liteResults("SC-1"), true), daemon.BoardView{})
	got := applyLocal(view, nil, nil, boardprefs.Prefs{}, vieweridentity.Identity{}, 0, board.LiveAgents{})

	if len(got.Cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(got.Cards))
	}
	if stage := got.Cards[0].Stage; stage != string(daemon.BoardBacklog) {
		t.Fatalf("stage = %q, want %q", stage, daemon.BoardBacklog)
	}
}
