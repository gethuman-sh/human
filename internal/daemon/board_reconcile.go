package daemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// BoardReconcileInterval is how often the durable reconcile pass re-scans open
// PM cards for orphaned handoffs after its immediate startup pass. Exported so
// the daemon wiring supplies it and tests can shorten it.
var BoardReconcileInterval = 2 * time.Minute

// ReconcileCard pairs an open PM ticket key with its comment thread, the input
// the reconcile pass derives a board placement from.
type ReconcileCard struct {
	Key      string
	Comments []tracker.Comment
}

// ReconcileLister enumerates the open PM cards to reconcile. Injected so the
// enumeration (tracker fan-out) stays in the command layer and the pass itself
// stays pure and testable.
type ReconcileLister func(ctx context.Context) ([]ReconcileCard, error)

// RunBoardReconcile is the durable counterpart to RunBoardFailureWatch's live
// fix→review chain. The live chain fires only on the one-shot Stop/SessionEnd
// hook event; if the daemon restarts or the hook is lost, that trigger is gone
// and a finished build's [human:ready-for-review] handoff sits forever with no
// review (SC-430). This pass re-scans comments — the state the hook store lost
// on restart survives in the tracker — and chains the review the live path
// missed.
//
// It runs one pass immediately at start (recovers a restart-orphaned handoff
// without waiting a full interval) then on a ticker, mirroring
// RunAgentZombieSweep. nil deps disable it.
func RunBoardReconcile(ctx context.Context, listCards ReconcileLister, chainReview func(pmKey string) error, interval time.Duration, logger zerolog.Logger) {
	if listCards == nil || chainReview == nil {
		return
	}

	logger.Info().Msg("board reconcile started")

	// Recover a restart-orphaned handoff immediately, before the first tick.
	reconcileOnce(ctx, listCards, chainReview, logger)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileOnce(ctx, listCards, chainReview, logger)
		}
	}
}

// reconcileOnce runs a single reconcile pass. A transient list error is logged
// and skipped so a momentary tracker blip never kills the loop.
func reconcileOnce(ctx context.Context, listCards ReconcileLister, chainReview func(pmKey string) error, logger zerolog.Logger) {
	cards, err := listCards(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("board reconcile: cannot list PM cards")
		return
	}
	if n := reconcileOrphanedHandoffs(cards, chainReview, logger); n > 0 {
		logger.Info().Int("launched", n).Msg("board reconcile: chained review for orphaned handoffs")
	}
}

// reconcileOrphanedHandoffs launches the missed review for every card whose
// latest implementation marker is a ready-for-review handoff with no
// subsequent review marker. It reuses DeriveBoardCard verbatim so detection can
// never disagree with the board's rendered state.
//
// The orphan condition is Stage == BoardImplementation && State == BoardDone.
// DeriveBoardCard picks the furthest stage carrying any marker, so any
// verification marker (review-started/complete/failed) would make the furthest
// stage verification — Stage == BoardImplementation therefore structurally
// means no verification marker exists at all. This subsumes ApplyFix's
// verification-running guard. And ApplyTransition re-loads live comments and
// no-ops when the target stage already has a running marker, so even if the
// live hook event and a reconcile tick race, the second call is a no-op at the
// transition layer — the two can never double-launch a review. Returns the
// number of reviews launched.
func reconcileOrphanedHandoffs(cards []ReconcileCard, chainReview func(pmKey string) error, logger zerolog.Logger) int {
	launched := 0
	for _, card := range cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.Stage != BoardImplementation || derived.State != BoardDone {
			continue
		}
		if err := chainReview(card.Key); err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).Msg("board reconcile: cannot chain review for orphaned handoff")
			continue
		}
		launched++
	}
	return launched
}
