package daemon

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// LateResultReconcileInterval is how often the pass re-scans open PM cards for
// a stage result that arrived after the stage was already marked failed.
// Slower than the board's other reconcile passes on purpose: this reconciles
// HISTORY, not liveness — nothing about a delayed detection costs a retry or
// leaves work undone (SC-3853).
var LateResultReconcileInterval = 5 * time.Minute

// lateResultPair names one stage's failure->started->success marker triple.
// Substages of the collapsed done-stage board column (pr-review, deploy) each
// get their own pair so a failure in one substage is never paired with an
// unrelated substage's success — the two are different runs even though
// DeriveBoardCard folds them into the same board column (SC-3853).
type lateResultPair struct {
	name    string
	stage   BoardStage
	failed  string
	started string
	success string
}

// lateResultPairs covers the stages the ticket's own evidence named (review,
// implementation, planning, pr-review) plus deploy, whose failed->deployed
// shape is also the confirm-shipped repair pass's shape — the reason rule 2
// (ShippedOutOfBandDeployedBody) exists at all.
var lateResultPairs = []lateResultPair{
	{name: "planning", stage: BoardPlanning, failed: PlanningFailedHeader, started: PlanningStartedHeader, success: PlanReadyHeader},
	{name: "implementation", stage: BoardImplementation, failed: ImplementationFailedHeader, started: ImplementationStartedHeader, success: ReadyForReviewHeader},
	{name: "review", stage: BoardVerification, failed: ReviewFailedHeader, started: ReviewStartedHeader, success: ReviewCompleteHeader},
	{name: "pr-review", stage: BoardDoneStage, failed: PRReviewFailedHeader, started: PRReviewStartedHeader, success: PRReviewPassedHeader},
	{name: "deploy", stage: BoardDoneStage, failed: DeployFailedHeader, started: DeployStartedHeader, success: DeployedHeader},
}

// LateResultCandidate is one occurrence of a stage's result arriving after the
// stage was already marked failed, with no relaunch in between.
type LateResultCandidate struct {
	Pair      string
	Stage     BoardStage
	FailedAt  time.Time
	SuccessAt time.Time
}

// lateResultCandidates walks comments in chronological order and, for each
// pair independently, tracks the most recent unresolved failure: a started
// marker clears it (a relaunch happened — the eventual success is the
// RETRY's result, not the same run finishing late), and a success marker
// while one is still pending is a late result. Scanning per-pair rather than
// per-stage is what keeps a pr-review failure from ever pairing with an
// unrelated deploy success even though both collapse to BoardDoneStage
// (SC-3853).
func lateResultCandidates(comments []tracker.Comment) []LateResultCandidate {
	sorted := make([]tracker.Comment, len(comments))
	copy(sorted, comments)
	sort.SliceStable(sorted, func(i, j int) bool { return commentNewer(sorted[j], sorted[i]) })

	var out []LateResultCandidate
	for _, pair := range lateResultPairs {
		var pendingFailure *tracker.Comment
		for i := range sorted {
			c := sorted[i]
			trimmed := strings.TrimSpace(c.Body)
			switch {
			case strings.HasPrefix(trimmed, pair.started):
				pendingFailure = nil
			case strings.HasPrefix(trimmed, pair.failed):
				cc := c
				pendingFailure = &cc
			case strings.HasPrefix(trimmed, pair.success):
				if pendingFailure != nil && !shippedOutOfBandDeploy(pair, trimmed) {
					out = append(out, LateResultCandidate{
						Pair:      pair.name,
						Stage:     pair.stage,
						FailedAt:  pendingFailure.Created,
						SuccessAt: c.Created,
					})
				}
				pendingFailure = nil
			}
		}
	}
	return out
}

// shippedOutOfBandDeploy reports whether body is the confirm-shipped repair
// pass's own [human:deployed] post (rule 2, SC-3853): it looks exactly like a
// deploy agent finishing after being marked failed, but the repair pass
// discovered an already-merged PR rather than observing a run complete.
func shippedOutOfBandDeploy(pair lateResultPair, body string) bool {
	return pair.name == "deploy" && strings.Contains(body, shippedOutOfBandSentinel)
}

// lateResultAlreadyReconciled reports whether a [human:late-result-reconciled]
// marker already covers cand — matched on pair name and the success comment's
// timestamp, which together identify the occurrence. Without this the pass
// would repost the same record every interval forever.
func lateResultAlreadyReconciled(comments []tracker.Comment, cand LateResultCandidate) bool {
	successKey := cand.SuccessAt.UTC().Format(time.RFC3339)
	for _, c := range comments {
		m, ok := marker.ParseBody(c.Body)
		if !ok || m.Type != MarkerLateResultReconciled {
			continue
		}
		if m.Fields["pair"] == cand.Pair && m.Fields["success_at"] == successKey {
			return true
		}
	}
	return false
}

// RunLateResultReconcile periodically scans open PM cards for a stage's result
// arriving after the stage had already been marked failed with no relaunch in
// between — the reaper (or an equivalent silence classifier) declaring a
// still-working agent dead. Each occurrence is recorded once via a
// [human:late-result-reconciled] marker so the ticket's own history explains
// the contradiction (acceptance criteria 2 and 4, SC-3853). nil list or
// commenterFor disables the pass (the package's "nil disables" convention).
func RunLateResultReconcile(ctx context.Context, list ReconcileLister, commenterFor CommenterFor, logger zerolog.Logger) {
	if list == nil || commenterFor == nil {
		return
	}
	logger.Info().Msg("late-result reconcile started")
	reconcileLateResultsOnce(ctx, list, commenterFor, logger)

	ticker := time.NewTicker(LateResultReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileLateResultsOnce(ctx, list, commenterFor, logger)
		}
	}
}

func reconcileLateResultsOnce(ctx context.Context, list ReconcileLister, commenterFor CommenterFor, logger zerolog.Logger) {
	cards, err := list(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("late-result reconcile: failed to list cards")
		return
	}
	for _, card := range cards {
		candidates := lateResultCandidates(card.Comments)
		if len(candidates) == 0 {
			continue
		}
		var commenter tracker.Commenter
		for _, cand := range candidates {
			if lateResultAlreadyReconciled(card.Comments, cand) {
				continue
			}
			if commenter == nil {
				commenter, err = commenterFor()
				if err != nil {
					logger.Warn().Err(err).Msg("late-result reconcile: no commenter")
					return
				}
			}
			body := LateResultReconciledBody(cand.Pair, cand.Stage, cand.FailedAt, cand.SuccessAt)
			if _, err := commenter.AddComment(ctx, card.Key, body); err != nil {
				logger.Warn().Err(err).Str("key", card.Key).Str("pair", cand.Pair).
					Msg("late-result reconcile: failed to post record")
				continue
			}
			logger.Info().Str("key", card.Key).Str("pair", cand.Pair).
				Msg("late-result reconcile: recorded a stage result that arrived after the stage was marked failed")
		}
	}
}
