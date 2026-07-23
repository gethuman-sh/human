package daemon

import (
	"context"
	"strings"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// FailureDiagnosis is the distilled cause of a dead agent run. It mirrors the
// agent package's type without importing it — the daemon package's agent
// collaborators are all interfaces wired in cmd/cmddaemon.
type FailureDiagnosis struct {
	Headline string
	Detail   string
}

// BoardFailureDiagnoser distills why a board agent's run died from its
// persisted execution artifacts. hookErrorType is the exit event's ErrorType
// ("" when it carried none). nil disables diagnosis (generic fallback).
type BoardFailureDiagnoser func(agentName, hookErrorType string) FailureDiagnosis

// genericStageFailure is the diagnosis-free failure line, kept for nil or
// empty-handed diagnosers so the marker never posts headerless.
const genericStageFailure = "agent exited without completing the stage"

// RunBoardFailureWatch watches for SessionEnd-style hook events from board
// agents and posts the stage's *-failed marker when an agent exits WITHOUT
// having posted its stage's done-marker. This closes the gap where an agent
// dies (or is killed) mid-stage: the board would otherwise show a stuck
// spinner forever. It mirrors RunAgentCleanup's subscribe loop.
//
// It is also the seam where the pipeline chains: a build that finishes
// cleanly (handoff posted) flows straight into its review via chainReview —
// no user gesture. Chaining rides the live SessionEnd event, never a
// comment-scan, so pre-existing handoffs are not retroactively reviewed on
// daemon start. nil chainReview disables chaining.
//
// commenterFor resolves the PM-role commenter lazily (per event) so the watcher
// holds no tracker handle across its lifetime; the PM commenter MUST be
// resolved by role, never by key prefix (both trackers may share a name).
// onHandoff, when non-nil, is fired with the exiting agent's name the moment
// its stage is observed to have ended cleanly (a done/handoff or terminal
// resolved marker). It is the success signal that authorizes reclaiming the
// run's private worktree — every other exit KEEPS the worktree so uncommitted
// work is never destroyed (SC-731). Best-effort/idempotent by contract.
func RunBoardFailureWatch(ctx context.Context, store *HookEventStore, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, daemonID string, logger zerolog.Logger) {
	if store == nil || commenterFor == nil {
		return
	}

	ch := store.Subscribe()
	defer store.Unsubscribe(ch)

	logger.Info().Msg("board failure watcher started")

	// Track events by monotonic sequence, not by agent name: board stage agents
	// reuse the same deterministic name on every rebuild, so a name-keyed
	// lifetime dedupe silently dropped every re-run's exit (SC-201). EventsSince
	// hands us each appended event exactly once and survives ring saturation.
	var lastSeq uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			newEvents, seq := store.EventsSince(lastSeq)
			lastSeq = seq
			for _, evt := range newEvents {
				if !strings.HasPrefix(evt.AgentName, "board-") {
					continue
				}
				if evt.EventName != "Stop" && evt.EventName != "SessionEnd" && evt.EventName != "StopFailure" {
					continue
				}
				go handleBoardAgentExit(ctx, evt.AgentName, evt.ErrorType, commenterFor, chainReview, reachable, commitsPresent, diagnose, onHandoff, retry, daemonID, logger)
			}
		}
	}
}

// handleBoardAgentExit posts the stage's *-failed marker unless the stage's
// latest marker is already its done-marker (a clean finish). A cleanly
// finished build chains into its review. Pulled out so the watch loop stays a
// thin event dispatcher.
func handleBoardAgentExit(ctx context.Context, agentName, errorType string, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, daemonID string, logger zerolog.Logger) {
	pmKey, stage, ok := parseAgentName(agentName)
	if !ok {
		return
	}
	commenter, err := commenterFor()
	if err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot resolve PM commenter")
		return
	}
	comments, err := commenter.ListComments(ctx, pmKey)
	if err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot list comments")
		return
	}
	// A clean stage finish leaves the stage's done-marker as the latest marker;
	// only treat the exit as a failure when that did NOT happen.
	_, state := latestStageState(comments, stage)
	if state == BoardDone {
		// A clean finish clears the automatic-retry budget: the next failure on
		// this stage is a fresh problem and deserves its own attempts, not the
		// remainder of an older one's.
		retry.reset(pmKey, stage)
		// A clean finish is the positive success signal: authorize reclaiming the
		// run's worktree (the work is safely committed on its branch).
		if onHandoff != nil {
			onHandoff(agentName)
		}
		if stage == BoardImplementation && chainReview != nil {
			// SC-782 merged verification stage: the autofix implementation container
			// now runs the review in-place (warm workspace, one container startup).
			// If it already posted a verification-stage marker, the review is
			// accounted for here — launching a second, cold review container would
			// re-run the whole suite. Branch on what that marker says:
			if vOK, vState := latestStageState(comments, BoardVerification); vOK {
				// review-complete (pass OR fail verdict) is a recorded outcome the
				// board acts on; a review-failed marker is already retryable. Either
				// way, do not chain a second review.
				if vState == BoardRunning {
					// The container died AFTER [human:review-started] but before the
					// review completed: surface a retryable review failure instead of
					// leaving the card spinning on a verification stage no agent owns.
					body := ReviewFailedHeader + "\nreview agent exited before completing the in-container review — retry the review"
					if _, err := commenter.AddComment(ctx, pmKey, StampDaemon(body, daemonID)); err != nil {
						logger.Warn().Err(err).Str("pm", pmKey).Msg("board merged-stage: cannot post review-failed after mid-review exit")
					}
				}
				return
			}
			chainReviewAfterBuild(ctx, pmKey, comments, commenter, chainReview, reachable, commitsPresent, daemonID, logger)
		}
		return
	}
	// Two more clean endings, neither a crash, both reclaimed like a handoff with
	// NO failed marker:
	//   1. A terminal BoardResolved marker with no handoff — implementation reaches
	//      it when triage concludes no fix is warranted ([human:no-fix-needed],
	//      ticket 405); planning when the work is already merged so there is nothing
	//      left to plan ([human:nothing-to-do], ticket 454). Stage-agnostic on
	//      purpose: BoardResolved is only ever produced by these terminal markers,
	//      never by a crash, so any stage that reaches it is a clean stop — scoping
	//      this to Implementation is what let the same defect class ship again on
	//      Planning.
	//   2. An open [human:options] block for the stage's OWN stage — a deliberate
	//      up-front human decision (see stagePausedOnOptions). Posting a *-failed
	//      here would red the card and loop re-planning forever (SC-751).
	if state == BoardResolved || stagePausedOnOptions(comments, stage) {
		retry.reset(pmKey, stage)
		if onHandoff != nil {
			onHandoff(agentName)
		}
		return
	}
	body := failedHeaderFor(stage) + "\n" + failureMarkerBody(diagnose, agentName, errorType)
	if _, err := commenter.AddComment(ctx, pmKey, StampDaemon(body, daemonID)); err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot post failed marker")
		// Without the failed marker the card does not derive to a failed state,
		// which is precisely what every in-place retry transition requires — so
		// an automatic relaunch would be rejected. Leave it for a human.
		return
	}
	// A stage that failed for a reason another attempt could fix — a flake, a
	// dead container — is relaunched here rather than waiting for someone to
	// click Retry. The failure stays on the record either way.
	retry.tryRelaunch(ctx, pmKey, stage, commenter, daemonID, logger)
}

// stagePausedOnOptions reports whether the exiting stage left an open
// [human:options] block naming its OWN stage — a deliberate up-front pause for a
// human decision, not a crash. The block stays open until the human picks
// (ApplyOption then relaunches this same stage with the choice injected).
// Posting a *-failed marker for such an exit would red the card and loop
// re-planning forever — the planning twin of the stranded-run class SC-731 fixed
// for worktrees (SC-751). openOptionsBlock's consumption rules guarantee the
// block belongs to THIS run: a later stage-started marker would have closed it.
// The check is stage-precise so an unrelated other-stage block never suppresses
// a real crash.
func stagePausedOnOptions(comments []tracker.Comment, stage BoardStage) bool {
	block, ok := openOptionsBlock(comments)
	if !ok {
		return false
	}
	optStage, _, _ := parseOptionsBlock(block.Body)
	return optStage == stage
}

// chainReviewAfterBuild flows a cleanly finished build into its review, guarding
// the chain twice: the handoff branch must resolve on this machine, and (when a
// commit gate is wired) the handoff's named commits must actually be present on
// that branch. Pulled out of handleBoardAgentExit so the exit handler stays a
// thin stage dispatcher and the chain's gates read as one unit.
func chainReviewAfterBuild(ctx context.Context, pmKey string, comments []tracker.Comment, commenter tracker.Commenter, chainReview func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, daemonID string, logger zerolog.Logger) {
	// Only chain a review for a branch this machine can resolve; a board-context
	// fix leaves its branch local on the machine that produced it, so a daemon
	// elsewhere must leave the handoff for one that can reach it rather than start
	// a review that can never check out the code (SC-652).
	branch := latestPrefixedLine(comments, ReadyForReviewHeader, "branch:")
	if reachable != nil && !reachable(branch) {
		logger.Debug().Str("pm", pmKey).Str("branch", branch).
			Msg("board chain: handoff branch unreachable on this machine, leaving for a daemon that can reach it")
		return
	}
	// Fail loudly on a phantom-commit handoff: a handoff naming commits absent from
	// the branch would bind a review/deploy against SHAs the branch never contained
	// (a retry that never pushed its work, 735). On the live chain a red card is the
	// loud failure the ticket asks for — re-run the fix rather than review nothing.
	if handoffNamesPhantomCommits(comments, branch, commitsPresent) {
		body := ImplementationFailedHeader +
			"\nhandoff names commits absent from branch " + branch + " on this machine — re-run the fix"
		if _, err := commenter.AddComment(ctx, pmKey, StampDaemon(body, daemonID)); err != nil {
			logger.Warn().Err(err).Str("pm", pmKey).Msg("board chain: cannot post phantom-commit failure")
		}
		return
	}
	if err := chainReview(pmKey); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Msg("board chain: cannot start review after build")
	}
}

// handoffNamesPhantomCommits reports whether the latest handoff names at least
// one commit that is not present on branch on this machine. A nil gate or a
// handoff with no commits line is never phantom.
func handoffNamesPhantomCommits(comments []tracker.Comment, branch string, commitsPresent CommitsPresent) bool {
	if commitsPresent == nil {
		return false
	}
	commits := ParseCommitsFromHandoff(latestHandoffBody(comments))
	return len(commits) > 0 && !commitsPresent(branch, commits)
}

// failureMarkerBody composes the failed marker's body: a one-line headline
// first (the card badge/tooltip reads exactly that line via failureReason),
// then a blank line and the markdown detail block for the detail pane. A nil
// or empty-handed diagnoser degrades to the pre-diagnosis generic line.
func failureMarkerBody(diagnose BoardFailureDiagnoser, agentName, errorType string) string {
	if diagnose == nil {
		return genericStageFailure
	}
	d := diagnose(agentName, errorType)
	if d.Headline == "" {
		return genericStageFailure
	}
	if d.Detail == "" {
		return d.Headline
	}
	return d.Headline + "\n\n" + d.Detail
}
