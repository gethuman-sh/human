package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// boardExitRecheckStep/boardExitRecheckTries bound the read-after-write race
// between a board agent's exit event and the tracker's comment thread catching
// up to a just-posted stage-completion marker (hand-off, stage-done, resolved).
// Mirrors the wait/retry shape of internal/agent/diagnose.go's waitForRunEnd.
// Package vars so tests can shrink them to keep the suite fast.
var (
	boardExitRecheckStep  = 2 * time.Second
	boardExitRecheckTries = 3
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
func RunBoardFailureWatch(ctx context.Context, store *HookEventStore, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, advancePRLoop func(pmKey, agentName, errorType string) error, advanceDeployFix func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, daemonID string, logger zerolog.Logger) {
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
				go handleBoardAgentExit(ctx, evt.AgentName, evt.ErrorType, evt.EventName, commenterFor, chainReview, advancePRLoop, advanceDeployFix, reachable, commitsPresent, diagnose, onHandoff, retry, daemonID, logger)
			}
		}
	}
}

// handleBoardAgentExit posts the stage's *-failed marker unless the stage's
// latest marker is already its done-marker (a clean finish). A cleanly
// finished build chains into its review. Pulled out so the watch loop stays a
// thin event dispatcher.
//
// eventName is the hook event's own name ("Stop", "SessionEnd", or the zombie
// sweep's synthesized "StopFailure") — the only signal that discriminates a
// clean exit-0 from a genuine death, since both carry an empty ErrorType. It
// is used to derive cleanExit, which in turn guards against misreading a
// clean finish that merely raced its own review-complete propagation as a
// mid-review crash (SC-2133).
func handleBoardAgentExit(ctx context.Context, agentName, errorType, eventName string, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, advancePRLoop func(pmKey, agentName, errorType string) error, advanceDeployFix func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, daemonID string, logger zerolog.Logger) {
	pmKey, stage, ok := parseAgentName(agentName)
	if !ok {
		return
	}
	// The deploy-fixer is not a board stage: its exit re-runs the deploy (on `done`)
	// or reds the card, driven by AdvanceDeployFix — not the generic stage-failure path.
	if driveDeployFixExit(pmKey, stage, agentName, advanceDeployFix, onHandoff, logger) {
		return
	}
	// The PR review→fix loop steps are not board stages: their exits are driven
	// by the loop executor, not the generic stage-failure path below.
	if drivePRLoopExit(pmKey, stage, agentName, errorType, advancePRLoop, onHandoff, logger) {
		return
	}
	commenter, err := commenterFor()
	if err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot resolve PM commenter")
		return
	}
	// A reap synthesizes "StopFailure" for a genuinely dead run; a clean exit-0
	// fires "Stop" or "SessionEnd" — both carry an empty ErrorType, so eventName
	// is the only thing that tells them apart. cleanExit gates the mid-review
	// death check below: a clean exit-0 that merely raced its own
	// review-complete propagation is never a death (SC-2133).
	cleanExit := eventName != "StopFailure"
	// A clean stage finish leaves the stage's done-marker as the latest marker;
	// only treat the exit as a failure when that did NOT happen. Re-read with
	// bounded backoff first: a reap-synthesized exit can be handled before the
	// just-posted hand-off comment is visible on the tracker (SC-1484's
	// read-after-write race) — polling briefly for a settled state closes that
	// window without changing behavior for a genuinely incomplete stage.
	comments, err := listStageSettled(ctx, commenter, pmKey, stage)
	if err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot list comments")
		return
	}
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
		if stage == BoardImplementation {
			chainReviewAfterCleanBuild(ctx, pmKey, agentName, errorType, cleanExit, comments, commenter, chainReview, reachable, commitsPresent, diagnose, daemonID, logger)
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

// chainReviewAfterCleanBuild handles a cleanly finished implementation stage's
// review chaining, guarding the SC-782 merged verification stage: the autofix
// implementation container now runs the review in-place (warm workspace, one
// container startup). If it already posted a verification-stage marker, the
// review is accounted for and a second cold review container must NOT launch;
// only a mid-review death (marker still running AND the exit itself was not
// clean) surfaces a retryable review failure, its body composed from diagnose
// exactly like the generic crash path so the tracker carries the real reason
// instead of a hardcoded sentence (SC-1688). A clean exit-0 (cleanExit) whose
// verification marker still reads "running" means the review-complete simply
// has not propagated to this read yet — never that the review died (SC-2133);
// listStageSettled's extended settle-wait already gives that propagation a
// bounded chance to land before this is reached, so a residual "running" read
// past that budget is treated as still-in-flight, not a crash. Otherwise it
// flows into chainReviewAfterBuild's branch/commit-gated chain. A nil
// chainReview disables chaining entirely.
func chainReviewAfterCleanBuild(ctx context.Context, pmKey, agentName, errorType string, cleanExit bool, comments []tracker.Comment, commenter tracker.Commenter, chainReview func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, daemonID string, logger zerolog.Logger) {
	if chainReview == nil {
		return
	}
	if vOK, vState := latestStageState(comments, BoardVerification); vOK {
		// review-complete (pass OR fail verdict) is a recorded outcome the board
		// acts on; a review-failed marker is already retryable. Either way, do not
		// chain a second review. Only a mid-review death — the marker still reads
		// "running" AND the exit itself was not clean — needs a retryable marker.
		if vState == BoardRunning && !cleanExit {
			body := ReviewFailedHeader + "\n" + failureMarkerBody(diagnose, agentName, errorType) +
				"\n\n" + handoffSearchNote(BoardVerification, ReviewCompleteHeader)
			if _, err := commenter.AddComment(ctx, pmKey, StampDaemon(body, daemonID)); err != nil {
				logger.Warn().Err(err).Str("pm", pmKey).Msg("board merged-stage: cannot post review-failed after mid-review exit")
			}
		}
		return
	}
	chainReviewAfterBuild(ctx, pmKey, comments, commenter, chainReview, reachable, commitsPresent, daemonID, logger)
}

// handoffSearchNote composes the "what was searched for" line the review-failed
// marker carries: a recorded failure must be diagnosable from the ticket alone,
// without reading agent logs, when the handoff it claims is missing really is
// absent (SC-2133 AC #3).
func handoffSearchNote(stage BoardStage, header string) string {
	return fmt.Sprintf("searched for: a %q marker on the %s stage — none found", header, stage)
}

// drivePRLoopExit routes a PR review/fix loop agent's exit to the loop driver
// instead of the generic stage-failure path, reclaiming its worktree first (the
// reviewer is read-only, the fixer already pushed its work). It reports whether
// the exit was a loop step and thus fully handled here. A non-loop stage returns
// false so the caller falls through to the stage-failure handling.
//
// The exiting run's identity travels with the call: a step that dies before
// recording its outcome can only be explained from its artifacts, and dropping
// the name here is what left the loop's escalation with nothing to say (SC-1892).
func drivePRLoopExit(pmKey string, stage BoardStage, agentName, errorType string, advancePRLoop func(pmKey, agentName, errorType string) error, onHandoff func(agentName string), logger zerolog.Logger) bool {
	if stage != prReviewAgentStage && stage != prFixAgentStage {
		return false
	}
	if onHandoff != nil {
		onHandoff(agentName)
	}
	if advancePRLoop != nil {
		if err := advancePRLoop(pmKey, agentName, errorType); err != nil {
			logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).Msg("board PR loop: advance failed")
		}
	}
	return true
}

// driveDeployFixExit routes a deploy-fixer's exit to AdvanceDeployFix, reclaiming
// its worktree first (the fixer already pushed its work). It reports whether the
// exit was the deploy-fix stage and thus fully handled here. A non-deployfix stage
// returns false so the caller falls through to the PR-loop / stage-failure handling.
func driveDeployFixExit(pmKey string, stage BoardStage, agentName string, advanceDeployFix func(pmKey string) error, onHandoff func(agentName string), logger zerolog.Logger) bool {
	if stage != deployFixAgentStage {
		return false
	}
	if onHandoff != nil {
		onHandoff(agentName)
	}
	if advanceDeployFix != nil {
		if err := advanceDeployFix(pmKey); err != nil {
			logger.Warn().Err(err).Str("pm", pmKey).Msg("board deploy fix: advance failed")
		}
	}
	return true
}

// stagePausedOnOptions reports whether the exiting stage left an open
// [human:options] block naming its OWN stage, or an EARLIER stage that
// answering the question would rework — either is a deliberate pause for a
// human decision, not a crash. The block stays open until the human picks
// (ApplyOption then relaunches the named stage with the choice injected).
// Posting a *-failed marker for such an exit would red the card and loop
// re-planning forever — the planning twin of the stranded-run class SC-731 fixed
// for worktrees (SC-751). openOptionsBlock's consumption rules guarantee the
// block belongs to THIS run: a later stage-started marker would have closed it.
// A block naming a stage the card has not yet reached is a stale or
// target-relaunch block, not a pause (SC-1669), so the rank check is
// at-or-before rather than equality — SC-1957's fix for the systematic loss of
// late-stage rework questions the strict equality caused.
func stagePausedOnOptions(comments []tracker.Comment, stage BoardStage) bool {
	block, ok := openOptionsBlock(comments)
	if !ok {
		return false
	}
	optStage, _, opts := parseOptionsBlock(block.Body)
	if len(opts) == 0 {
		return false
	}
	return stageRank[optStage] <= stageRank[stage]
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

// listStageSettled fetches the PM ticket's comment thread and, while the given
// stage has not yet reached a settled (non-failure) state, re-fetches with a
// bounded backoff — closing the window where an exit event is handled before
// the tracker reflects the stage's just-posted completion marker. It returns
// as soon as the thread looks settled or the retry budget is spent, so a
// genuinely incomplete stage pays the full backoff exactly once. A mid-loop
// list error keeps the last good snapshot (the caller then decides on stale
// data rather than erroring out); ctx cancellation returns the last snapshot
// read so far.
func listStageSettled(ctx context.Context, commenter tracker.Commenter, pmKey string, stage BoardStage) ([]tracker.Comment, error) {
	comments, err := commenter.ListComments(ctx, pmKey)
	if err != nil {
		return nil, err
	}
	for try := 0; !stageSettled(comments, stage) && try < boardExitRecheckTries-1; try++ {
		select {
		case <-ctx.Done():
			return comments, nil
		case <-time.After(boardExitRecheckStep):
		}
		// A re-read failure mid-backoff keeps the last good snapshot rather than
		// discarding it — the only way the caller ever gets an error is the very
		// first read failing, so a later flake never turns a real hand-off into a
		// dropped decision.
		if c, e := commenter.ListComments(ctx, pmKey); e == nil {
			comments = c
		}
	}
	return comments, nil
}

// stageSettled reports whether the stage's latest marker is one of the three
// clean, non-failure endings handleBoardAgentExit treats as settled: the
// stage's own done-marker, a terminal BoardResolved marker, or an open
// same-stage [human:options] pause. It is no longer the exact negation of the
// failure branch: for the implementation stage it ALSO keeps re-reading while
// the merged verification stage looks in-flight (SC-782's autofix container
// runs the review in-place and can post its [human:review-complete] handoff a
// moment after the implementation's own done-marker). Giving that second,
// later handoff the same settle-wait chance the first one gets closes the
// SC-2133 race where a clean exit's read lands between the two handoffs and
// misreads the still-propagating review as a mid-review death.
func stageSettled(comments []tracker.Comment, stage BoardStage) bool {
	_, state := latestStageState(comments, stage)
	if state != BoardDone && state != BoardResolved && !stagePausedOnOptions(comments, stage) {
		return false
	}
	if stage == BoardImplementation && verificationInFlight(comments) {
		return false
	}
	return true
}

// verificationInFlight reports whether the verification stage's latest marker
// is [human:review-started] with no later review-complete/review-failed —
// the merged-container review is running but has not yet recorded an outcome.
func verificationInFlight(comments []tracker.Comment) bool {
	ok, state := latestStageState(comments, BoardVerification)
	return ok && state == BoardRunning
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
