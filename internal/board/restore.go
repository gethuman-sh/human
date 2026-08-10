package board

import "github.com/gethuman-sh/human/internal/daemon"

// RestoreStages puts each card back in the column the last known board had it
// in.
//
// It exists for the quick paint, and only there: that path fetches titles
// without the per-ticket comment scan, so no card carries a derived stage and
// composedStage falls every open ticket to Backlog. A board whose work is in
// Verification and Implementation therefore opens as one Backlog column and
// rearranges itself a moment later, which reads as the board being wrong rather
// than as it still loading (SC-4324).
//
// The join is deliberate about which half it trusts for what. The ticket SET
// comes from view, because that is the live fetch and the only thing that knows
// a ticket was created or closed since; the PLACEMENT comes from last, because
// a stale column is still the best answer available before the scan resolves.
// A key the snapshot never saw keeps its default, and a key the snapshot has but
// the fetch does not is dropped with it — a closed ticket must not be
// resurrected by a snapshot taken before it closed.
//
// Only Stage is restored. State, Verdict and the rest stay as composed: the
// quick paint marks every card as still resolving, and a stale "running" badge
// under that spinner would be indistinguishable from a live one.
//
// Callers pass a zero BoardView for last when no snapshot is available, which
// leaves the view exactly as composed.
func RestoreStages(view daemon.BoardView, last daemon.BoardView) daemon.BoardView {
	if len(last.Cards) == 0 || len(view.Cards) == 0 {
		return view
	}
	stages := make(map[string]string, len(last.Cards))
	for _, card := range last.Cards {
		if card.Stage != "" {
			stages[card.Key] = card.Stage
		}
	}
	for i := range view.Cards {
		if stage, ok := stages[view.Cards[i].Key]; ok {
			view.Cards[i].Stage = stage
		}
	}
	return view
}
