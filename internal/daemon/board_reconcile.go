package daemon

import (
	"context"
	"math/rand"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// BoardReconcileInterval is how often the durable reconcile pass re-scans open
// PM cards for orphaned handoffs after its immediate startup pass. Exported so
// the daemon wiring supplies it and tests can shorten it.
var BoardReconcileInterval = 2 * time.Minute

// StuckRunningGrace is how long a card may sit in a running state before the
// stuck-running reconcile pass is willing to red it. It spares a genuinely slow
// but live agent — only a running-state card older than this AND with no live
// stage agent is treated as a dead-end.
var StuckRunningGrace = 15 * time.Minute

// BoardReconcileJitter is the fraction of the interval added/subtracted at
// random each cycle so independently started daemons do not converge on the
// same reconcile instant and stampede one orphaned handoff (SC-660 rule 6).
var BoardReconcileJitter = 0.5

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

// BranchReachable reports whether a handoff branch resolves on THIS machine —
// as a local ref or on origin. A board-context fix leaves its branch local on
// the machine that produced it, so a daemon on another machine cannot serve a
// review for it; gating the review chain on reachability leaves such a handoff
// for a daemon that can reach the branch. A nil predicate disables the gate
// (every branch treated as reachable), matching the package's "nil disables"
// convention for optional deps.
type BranchReachable func(branch string) bool

// CommitsPresent reports whether every named commit is reachable from branch on
// THIS machine (local ref or origin/<branch>). It layers on BranchReachable: a
// handoff must not merely name a branch this machine can resolve, but a branch
// that actually CONTAINS the commits it binds a review/deploy against — a
// retry's handoff naming SHAs that were never pushed anywhere is the failure it
// guards (735). A nil predicate disables the gate, matching the package's "nil
// disables" convention for optional deps.
type CommitsPresent func(branch string, commits []string) bool

// PRMergedProbe reports whether the pull request identified by prURL has been
// merged on the forge — the "confirmed shipped" signal for an out-of-band
// manual merge that posted no marker (SC-910). A nil probe disables the
// shipped-confirmation pass (the package's "nil disables" convention).
type PRMergedProbe func(ctx context.Context, prURL string) (bool, error)

// DeployedPoster posts a [human:deployed] marker (carrying the pr: line) on the
// PM ticket so DeriveBoardCard's supersession guard retires the stale
// deploy-failed red. A nil poster disables the shipped-confirmation pass.
type DeployedPoster func(ctx context.Context, pmKey, prURL string) error

// LiveAgentLister returns the names of the board agents currently running on
// THIS machine — the same source the zombie sweep reads. The stuck-running
// reconcile pass uses it to tell a genuinely-working (slow) run from a
// dead-ended card that froze with no live owner. A nil lister disables the
// pass (the package's "nil disables" convention): a card whose liveness cannot
// be established is never reddened.
type LiveAgentLister func() ([]string, error)

// FailedMarkerPoster posts a free-form *-failed marker body on the PM ticket,
// moving the card to a failed/needs-attention badge whose first body line is
// the headline. A nil poster disables the stuck-running pass.
type FailedMarkerPoster func(ctx context.Context, pmKey, body string) error

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
func RunBoardReconcile(ctx context.Context, listCards ReconcileLister, reachable BranchReachable, commitsPresent CommitsPresent, mergedProbe PRMergedProbe, postDeployed DeployedPoster, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, chainReview func(pmKey string) error, retry StageRetry, daemonID string, interval time.Duration, logger zerolog.Logger) {
	if listCards == nil || chainReview == nil {
		return
	}

	logger.Info().Msg("board reconcile started")

	// Recover a restart-orphaned handoff immediately, before the first wait. The
	// jitter applies only to subsequent cycles, so a restart-orphan is never made
	// to wait a full interval.
	reconcileOnce(ctx, listCards, reachable, commitsPresent, mergedProbe, postDeployed, liveAgents, postFailed, chainReview, retry, daemonID, logger)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitteredInterval(interval, BoardReconcileJitter)):
			reconcileOnce(ctx, listCards, reachable, commitsPresent, mergedProbe, postDeployed, liveAgents, postFailed, chainReview, retry, daemonID, logger)
		}
	}
}

// jitteredInterval returns d randomly perturbed by up to ±d*fraction, floored
// at zero, so N independently started daemons spread their reconcile wake-ups
// instead of firing on the same wall-clock tick. A non-positive fraction
// returns d unchanged.
func jitteredInterval(d time.Duration, fraction float64) time.Duration {
	if fraction <= 0 {
		return d
	}
	delta := (rand.Float64()*2 - 1) * fraction * float64(d) // #nosec G404 -- scheduling jitter, not security
	j := time.Duration(float64(d) + delta)
	if j < 0 {
		return 0
	}
	return j
}

// reconcileOnce runs a single reconcile pass. A transient list error is logged
// and skipped so a momentary tracker blip never kills the loop.
func reconcileOnce(ctx context.Context, listCards ReconcileLister, reachable BranchReachable, commitsPresent CommitsPresent, mergedProbe PRMergedProbe, postDeployed DeployedPoster, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, chainReview func(pmKey string) error, retry StageRetry, daemonID string, logger zerolog.Logger) {
	cards, err := listCards(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("board reconcile: cannot list PM cards")
		return
	}
	if n := reconcileOrphanedHandoffs(cards, reachable, commitsPresent, chainReview, logger); n > 0 {
		logger.Info().Int("launched", n).Msg("board reconcile: chained review for orphaned handoffs")
	}
	if n := reconcileShippedFailures(ctx, cards, mergedProbe, postDeployed, logger); n > 0 {
		logger.Info().Int("cleared", n).Msg("board reconcile: confirmed shipped, cleared stale deploy-failed red")
	}
	if n := reconcileStuckRunning(ctx, cards, liveAgents, postFailed, retry, daemonID, time.Now(), logger); n > 0 {
		logger.Info().Int("reddened", n).Msg("board reconcile: reddened stuck-running cards with no live agent")
	}
}

// reconcileStuckRunning reds the dead-end a NOT DONE bug-verify (and any other
// silently-halted stage) leaves behind: a card frozen in a running state with
// no terminal marker and no live agent. The live exit-hook watcher and the
// container-only zombie sweep both miss it on a daemon restart or a dropped
// hook, so the card sits at "being fixed" forever (1136). This is the durable
// safety net — the bug-fix analog of the no-dead-end-states work (SC-355/591).
//
// A card is reddened only when ALL hold: its derived state is BoardRunning; its
// stage has a *-failed marker (Planning/Implementation/Verification/Done); it
// has sat past StuckRunningGrace; and no board agent for (key, stage) is alive
// on this machine. The grace plus the liveness probe spare a genuinely slow but
// live run — only a card with no owner is failed. Nil deps or a lister error do
// nothing: the pass never reds a card it cannot prove is dead. Idempotent —
// once the *-failed marker lands the card derives BoardFailed, so the next tick
// skips it and never double-posts. Reuses DeriveBoardCard verbatim so detection
// can never disagree with the board's rendered state. Returns the number reddened.
func reconcileStuckRunning(ctx context.Context, cards []ReconcileCard, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, retry StageRetry, daemonID string, now time.Time, logger zerolog.Logger) int {
	if liveAgents == nil || postFailed == nil {
		return 0
	}
	names, err := liveAgents()
	if err != nil {
		// Without a trustworthy liveness picture the pass must not red anything —
		// a probe blip is not evidence a card is dead.
		logger.Warn().Err(err).Msg("board reconcile: cannot list live agents, leaving running cards as-is")
		return 0
	}
	alive := make(map[string]struct{}, len(names))
	for _, n := range names {
		alive[n] = struct{}{}
	}

	reddened := 0
	for _, card := range cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.State != BoardRunning {
			continue
		}
		header := failedHeaderFor(derived.Stage)
		if header == "" {
			continue
		}
		if now.Sub(derived.StageEnteredAt) < StuckRunningGrace {
			// Young enough to still be genuine in-flight work.
			continue
		}
		if _, ok := alive[agentNameFor(card.Key, derived.Stage)]; ok {
			// A live owner is working this stage — slow, not stuck.
			continue
		}
		body := header + "\n" + stuckRunningReason(derived.Stage)
		if err := postFailed(ctx, card.Key, body); err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).
				Msg("board reconcile: cannot red stuck-running card")
			continue
		}
		reddened++
		// This is the fallback path the live failure watcher misses — an agent
		// that died with no exit hook (a daemon restart, a dropped event). The
		// same bounded relaunch applies, so a silently-dead stage recovers here
		// too instead of only reddening. The just-posted failed marker is the
		// trail record, so no separate retry note (nil commenter); the shared
		// per-stage budget bounds this path and the watcher's together.
		retry.tryRelaunch(ctx, card.Key, derived.Stage, nil, daemonID, logger)
	}
	return reddened
}

// stuckRunningReason is the one-line badge text for a card the stuck-running
// pass red: the stage froze with no terminal marker and no live agent, so it
// needs attention (a Retry). The first body line becomes the card's headline.
func stuckRunningReason(stage BoardStage) string {
	return "Stuck in " + string(stage) + ": no terminal marker and no live agent — needs attention"
}

// reconcileShippedFailures clears the 695-class stale red: a done-stage card
// whose newest marker is a deploy-failure but whose PR the forge reports merged
// (an out-of-band manual merge posted no marker). For each such card it posts a
// [human:deployed] marker; DeriveBoardCard's existing supersession guard then
// retires the failure on the next derivation. Reuses DeriveBoardCard verbatim so
// detection can never disagree with the board's rendered state. nil deps disable
// the pass. Returns the number of cards cleared.
func reconcileShippedFailures(ctx context.Context, cards []ReconcileCard, mergedProbe PRMergedProbe, postDeployed DeployedPoster, logger zerolog.Logger) int {
	if mergedProbe == nil || postDeployed == nil {
		return 0
	}
	cleared := 0
	for _, card := range cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		// Only a done-stage failure that names a PR can be confirmed shipped: the
		// out-of-band merge posts no marker, so the forge's merged flag is the only
		// evidence the work landed.
		if derived.State != BoardFailed || derived.Stage != BoardDoneStage || derived.PRURL == "" {
			continue
		}
		merged, err := mergedProbe(ctx, derived.PRURL)
		if err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).Str("pr", derived.PRURL).
				Msg("board reconcile: cannot probe PR merge status, leaving card as-is")
			continue
		}
		if !merged {
			// A genuinely-open failure must stay red — never clear on unknown state.
			continue
		}
		if err := postDeployed(ctx, card.Key, derived.PRURL); err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).
				Msg("board reconcile: cannot post deployed marker for shipped PR")
			continue
		}
		cleared++
	}
	return cleared
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
// transition layer — the two can never double-launch a review.
//
// A reachability gate guards the chain: a review is chained only for a handoff
// whose branch this machine can resolve (local ref or on origin). A board-context
// fix leaves its branch local on the machine that produced it, so a daemon on
// another machine skips that handoff and leaves it for one that can reach the
// branch — never starting a review it could never satisfy (SC-652). Returns the
// number of reviews launched.
func reconcileOrphanedHandoffs(cards []ReconcileCard, reachable BranchReachable, commitsPresent CommitsPresent, chainReview func(pmKey string) error, logger zerolog.Logger) int {
	launched := 0
	for _, card := range cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.Stage != BoardImplementation || derived.State != BoardDone {
			continue
		}
		// A daemon only chains a review for a branch it can actually resolve on
		// this machine; a handoff branch left local on another machine is left for
		// a daemon that can reach it, rather than starting a review that can never
		// check out the code (SC-652).
		if reachable != nil && !reachable(derived.Branch) {
			logger.Debug().Str("pm", card.Key).Str("branch", derived.Branch).
				Msg("board reconcile: handoff branch unreachable on this machine, leaving for a daemon that can reach it")
			continue
		}
		// Skip-and-leave on a phantom-commit handoff: the durable reconcile pass is
		// a periodic scan that must not red a card another machine can legitimately
		// serve, so an unverifiable handoff is left rather than failed (735). The
		// loud failure lives on the live chain (board_failure.go).
		if handoffNamesPhantomCommits(card.Comments, derived.Branch, commitsPresent) {
			logger.Warn().Str("pm", card.Key).Str("branch", derived.Branch).
				Msg("board reconcile: handoff names commits absent from branch on this machine, leaving it")
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
