package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/proxy"
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

// LatestOutcomeClass reports the most recent model-call outcome class recorded
// at the network boundary for a (ticket, stage), and whether any was recorded.
// It is the read seam ModelOutcomeSink.LatestClass provides, injected as a
// function field (mirroring diagnose) so the failure path can name why a run
// failed from the live boundary without the daemon package threading its sink
// type through the proxy. A nil value disables the enrichment, leaving the
// failure marker byte-for-byte as it was before (SC-2555 step 5b).
type LatestOutcomeClass func(ticket, stage string) (string, bool)

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
func RunBoardFailureWatch(ctx context.Context, store *HookEventStore, runs *RunRegistry, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, liveAgents LiveAgentLister, advancePRLoop func(pmKey, agentName, errorType string) error, advanceDeployFix func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, latestClass LatestOutcomeClass, daemonID string, logger zerolog.Logger) {
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
				go handleBoardAgentExit(ctx, runs, evt.RunID, evt.AgentName, evt.ErrorType, evt.EventName, commenterFor, chainReview, liveAgents, advancePRLoop, advanceDeployFix, reachable, commitsPresent, diagnose, onHandoff, retry, latestClass, daemonID, logger)
			}
		}
	}
}

// claimExit decides whether this event is one this daemon may act on, and what
// work it is for. It answers from the daemon's OWN launch record rather than from
// the event, and consumes the record so only the first exit of a run acts.
//
// The event is not a trustworthy statement of what it is. Its agent name is
// filled from HUMAN_AGENT_NAME inside a container and forwarded over a route
// authenticated only by the shared daemon token — which every agent container
// holds — and the ticket key was then parsed straight out of that name. Anything
// able to send one hook event could red a ticket and relaunch its stage (SC-4082).
// A run id the registry does not hold is, by construction, not a run this daemon
// started.
//
// It is also the exactly-once gate. One run can raise several events that all
// look like its exit — a StopFailure on an API error and a Stop when the turn
// ends, and a Stop may follow a StopFailure by contract — which drove the loop
// twice and posted the same escalation twice on SC-3613. Claim removes the
// record, so every later event for that run finds nothing.
//
// A run with NO id predates the registry: it was launched by a daemon without
// this code, so its exit falls back to the name-derived identity rather than
// being dropped. That window is one daemon restart wide and closes itself; the
// alternative — dropping those exits — would strand every in-flight card across
// an upgrade for the sake of a case that resolves in minutes.
func claimExit(runs *RunRegistry, runID, agentName string, logger zerolog.Logger) (string, BoardStage, bool) {
	if runID == "" {
		pmKey, stage, ok := parseAgentName(agentName)
		if ok && runs != nil {
			logger.Debug().Str("agent", agentName).
				Msg("board exit: no run id on the event; falling back to the name (a run launched before this daemon started)")
		}
		return pmKey, stage, ok
	}
	rec, ok := runs.Claim(runID)
	if !ok {
		// Loud on purpose: this is either a second event for a run already
		// handled, or an event naming work this daemon never launched. Both are
		// worth seeing, and neither may move a ticket.
		logger.Info().Str("agent", agentName).
			Msg("board exit: ignoring an event for a run this daemon did not launch, or has already handled")
		return "", "", false
	}
	return rec.PMKey, rec.Stage, true
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
func handleBoardAgentExit(ctx context.Context, runs *RunRegistry, runID, agentName, errorType, eventName string, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, liveAgents LiveAgentLister, advancePRLoop func(pmKey, agentName, errorType string) error, advanceDeployFix func(pmKey string) error, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, latestClass LatestOutcomeClass, daemonID string, logger zerolog.Logger) {
	pmKey, stage, ok := claimExit(runs, runID, agentName, logger)
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
	if handleCleanStageEnding(ctx, pmKey, stage, agentName, errorType, cleanExit, comments, commenter, chainReview, liveAgents, reachable, commitsPresent, diagnose, onHandoff, retry, daemonID, logger) {
		return
	}
	// A refusal that kills the agent before it records an exit (the SC-2856
	// incident: a session-limit refusal) is classified from the hook errorType
	// and the model-boundary class BEFORE the outage gate below, so it is
	// recognised even when retry.recordedOutage sees nothing.
	kind, reason := classifyUnavailability(errorType, latestClass, pmKey, string(stage))
	if handleOutageExit(ctx, pmKey, stage, agentName, errorType, comments, commenter, diagnose, retry, kind, reason, daemonID, logger) {
		return
	}
	if handleNeedsPersonExit(ctx, pmKey, stage, agentName, kind, reason, commenter, logger) {
		return
	}
	// A silence reap (the zombie sweep reaping an agent that went quiet — no
	// hook event and no outstanding model request — past its idle budget) is a
	// machine-chosen stop, not a stage failure: the card still reads red (the
	// stage did not finish), but the retry budget must not be charged for it and
	// the trail must say plainly what was observed and why (SC-2447/SC-3074).
	// Checked before the generic failure path so the sentinel never falls
	// through to the charged branch below.
	if handleSilenceReapExit(ctx, pmKey, stage, agentName, errorType, comments, commenter, retry, logger) {
		return
	}
	body := failedHeaderFor(stage) + "\n" + appendModelOutcomeNote(failureMarkerBody(diagnose, agentName, errorType), latestClass, pmKey, string(stage))
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
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

// handleCleanStageEnding deals with every way a stage exit is NOT a failure, and
// reports whether it handled the exit. Split out of handleBoardAgentExit so that
// function stays inside the complexity gate: the clean endings are one subject
// and they carry most of its branches.
//
// Three shapes, all reclaimed like a handoff with NO failed marker:
//
//  1. The stage's own done-marker is latest — the ordinary clean finish.
//  2. A terminal BoardResolved marker with no handoff: implementation reaches it
//     when triage concludes no fix is warranted ([human:no-fix-needed], ticket
//     405); planning when the work is already merged so there is nothing left to
//     plan ([human:nothing-to-do], ticket 454). Stage-agnostic on purpose —
//     BoardResolved is only ever produced by these terminal markers, never by a
//     crash, so any stage that reaches it is a clean stop. Scoping this to
//     Implementation is what let the same defect class ship again on Planning.
//  3. An open [human:options] block for the stage's OWN stage — a deliberate
//     up-front human decision (see stagePausedOnOptions) — or a recorded
//     deliberate stop verdict from any gate (deliberateStopRecorded), a gate
//     concluding the ticket must not be worked. Posting a *-failed for either
//     would red the card and loop re-planning forever (SC-751). The verdict case
//     is stage-agnostic for the same reason as (2): the ticket-review gate runs
//     under PLANNING but files its verdict under BACKLOG, so scoping it to the
//     running stage is exactly what let this defect class recur (SC-2302).
//
// endedDeliberately reports the clean endings that are not the stage's own
// done-marker: a terminal resolution, a pause on an open decision, or a gate's
// recorded stop verdict. Kept beside its only caller as a named condition so the
// caller reads as "clean, or deliberately ended" rather than as a negated
// disjunction.
func endedDeliberately(comments []tracker.Comment, stage BoardStage, state BoardState) bool {
	return state == BoardResolved ||
		stagePausedOnOptions(comments, stage) ||
		deliberateStopRecorded(comments)
}

func handleCleanStageEnding(ctx context.Context, pmKey string, stage BoardStage, agentName, errorType string, cleanExit bool, comments []tracker.Comment, commenter tracker.Commenter, chainReview func(pmKey string) error, liveAgents LiveAgentLister, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, onHandoff func(agentName string), retry StageRetry, daemonID string, logger zerolog.Logger) bool {
	_, state := latestStageState(comments, stage)
	clean := state == BoardDone
	if !clean && !endedDeliberately(comments, stage, state) {
		return false
	}
	// A clean finish clears the automatic-retry budget: the next failure on this
	// stage is a fresh problem and deserves its own attempts, not the remainder
	// of an older one's.
	retry.reset(pmKey, stage)
	// It is also the positive success signal: authorize reclaiming the run's
	// worktree (the work is safely committed on its branch).
	if onHandoff != nil {
		onHandoff(agentName)
	}
	if clean && stage == BoardImplementation {
		chainReviewAfterCleanBuild(ctx, pmKey, agentName, errorType, cleanExit, comments, commenter, chainReview, liveAgents, reachable, commitsPresent, diagnose, daemonID, logger)
	}
	return true
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
// past that budget is treated as still-in-flight, not a crash. The mid-review
// death post is additionally gated on verificationAgentAlive (SC-3156): a
// distinct board-<key>-verification agent still running means the CHAINED
// topology, not the merged one — that reviewer owns its own liveness and its
// own exit is the correct evidence, so this path posts nothing and lets it be.
// Otherwise it flows into chainReviewAfterBuild's branch/commit-gated chain. A
// nil chainReview disables chaining entirely.
func chainReviewAfterCleanBuild(ctx context.Context, pmKey, agentName, errorType string, cleanExit bool, comments []tracker.Comment, commenter tracker.Commenter, chainReview func(pmKey string) error, liveAgents LiveAgentLister, reachable BranchReachable, commitsPresent CommitsPresent, diagnose BoardFailureDiagnoser, daemonID string, logger zerolog.Logger) {
	if chainReview == nil {
		return
	}
	if vOK, vState := latestStageState(comments, BoardVerification); vOK {
		// review-complete (pass OR fail verdict) is a recorded outcome the board
		// acts on; a review-failed marker is already retryable. Either way, do not
		// chain a second review. Only a mid-review death — the marker still reads
		// "running" AND the exit itself was not clean — needs a retryable marker.
		if vState == BoardRunning && !cleanExit {
			// A stage is judged dead only on evidence about that stage. In the
			// chained topology board-<key>-verification is a separate,
			// independently-alive agent in its own container; the exiting
			// implementation agent's reap says nothing about it — so if that
			// reviewer is alive, post nothing and let its own exit or the reconcile
			// pass adjudicate (SC-3156). Only when no verification agent is alive is
			// this the SC-782 merged case, where the exiting implementation
			// container WAS the reviewer and `agentName` therefore correctly names
			// the run that failed the review.
			if verificationAgentAlive(pmKey, liveAgents, logger) {
				return
			}
			body := ReviewFailedHeader + "\n" + failureMarkerBody(diagnose, agentName, errorType) +
				"\n\n" + handoffSearchNote(BoardVerification, ReviewCompleteHeader)
			if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
				logger.Warn().Err(err).Str("pm", pmKey).Msg("board merged-stage: cannot post review-failed after mid-review exit")
			}
		}
		return
	}
	chainReviewAfterBuild(ctx, pmKey, comments, commenter, chainReview, reachable, commitsPresent, daemonID, logger)
}

// handleNeedsPersonExit deals with a wall that does not self-heal (a revoked
// credential, exhausted billing): it still needs a person, but must never
// charge the retry budget or auto-relaunch — the next attempt would hit the
// exact same wall. Reports whether it handled the exit. Split out of
// handleBoardAgentExit so this branch's complexity costs its own function
// rather than the dispatcher's (SC-3024).
func handleNeedsPersonExit(ctx context.Context, pmKey string, stage BoardStage, agentName string, kind endingKind, reason string, commenter tracker.Commenter, logger zerolog.Logger) bool {
	if kind != endingNeedsPerson {
		return false
	}
	header := failedHeaderFor(stage)
	if header == "" {
		return false
	}
	body := header + "\n" + needsPersonReason(reason)
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot post needs-person marker")
	}
	return true
}

// handleSilenceReapExit deals with a silence-reap exit (errorType carries the
// ReapSilenceErrorType sentinel): a machine-chosen stop, not a stage failure,
// so it must never consume the ticket's retry budget. Reports whether it
// handled the exit, so the caller falls through to the generic charged
// failure path for anything else. Repeated stops on one stage are bounded and
// visible: at or over MaxSilenceReaps this posts a give-up marker instead of
// relaunching, naming the count once — silenceReapGaveUp dedups a second
// daemon reaching the same cap. Split out of handleBoardAgentExit so this
// branch's complexity costs its own function rather than the dispatcher's.
func handleSilenceReapExit(ctx context.Context, pmKey string, stage BoardStage, agentName, errorType string, comments []tracker.Comment, commenter tracker.Commenter, retry StageRetry, logger zerolog.Logger) bool {
	idle, ok := silenceReapIdle(errorType)
	if !ok || !retry.enabled() {
		return false
	}
	header := failedHeaderFor(stage)
	if header == "" {
		return false
	}
	if silenceReapGaveUp(comments, stage) {
		return true
	}
	stops := silenceReapCount(comments, stage) + 1
	if stops > MaxSilenceReaps {
		body := header + "\n" + silenceReapGiveUpReason(stage, stops)
		if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
			logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot post silence-reap give-up marker")
		}
		return true
	}
	body := header + "\n" + silenceReapReason(idle)
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot post silence-reap marker")
		return true
	}
	retry.relaunchSilenceReap(pmKey, stage, logger)
	return true
}

// verificationAgentAlive reports whether a distinct board-<pmKey>-verification
// agent is running on this machine — the chained-topology signal. When it is,
// the review owns its own liveness and its own exit adjudicates it; the
// implementation agent's exit must say nothing about it. A nil lister or a lookup
// error cannot establish separateness, so it degrades to the SC-782 merged
// assumption (the exiting implementation container WAS the reviewer), preserving
// today's behavior — the package's "nil disables" convention.
func verificationAgentAlive(pmKey string, liveAgents LiveAgentLister, logger zerolog.Logger) bool {
	if liveAgents == nil {
		return false
	}
	names, err := liveAgents()
	if err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Msg("board review-liveness: cannot list live agents")
		return false
	}
	want := agentNameFor(pmKey, BoardVerification)
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// handoffSearchNote composes the "what was searched for" line the review-failed
// marker carries: a recorded failure must be diagnosable from the ticket alone,
// without reading agent logs, when the handoff it claims is missing really is
// absent (SC-2133 AC #3).
func handoffSearchNote(stage BoardStage, header string) string {
	return fmt.Sprintf("searched for: a %q marker on the %s stage — none found", header, stage)
}

// drivePRLoopExit routes a PR review/fix loop agent's exit to the loop driver
// instead of the generic stage-failure path. It reports whether the exit was a
// loop step and thus fully handled here. A non-loop stage returns false so the
// caller falls through to the stage-failure handling.
//
// The exiting run's identity travels with the call: a step that dies before
// recording its outcome can only be explained from its artifacts, and dropping
// the name here is what left the loop's escalation with nothing to say (SC-1892).
//
// A SUBSTRATE FAILURE IS NOT AN EXIT (SC-4026). The hook fires StopFailure on a
// model API error, and Claude Code retries through it — the run carries on. The
// loop used to drive on that event regardless, read a verdict the reviewer had
// not written yet, and red the card while the review was still working: measured
// on SC-3613, a server_error at 08:42:16 escalated at 08:42:25, and the reviewer
// went on to record `approved` at 08:43:39 and exit 0 at 08:44:06.
//
// Liveness cannot decide this. The hook runs inside the claude process, so asking
// the container whether claude is alive races the process's own exit and answers
// "yes" for a clean finish as readily as for a retry. The error type is the fact
// that is actually available, and classifyUnavailability already knows how to
// read it — the loop simply never consulted it, because this function returns
// long before the generic path's classification (board_failure.go, below).
//
// A run that genuinely dies on a substrate failure is not stranded: the durable
// reconcile pass re-drives a loop card whose half-agent is gone, on the machine
// that owns it. Waiting for that is strictly better than escalating a review that
// is still running, because the reconcile answer is right in both cases.
//
// The worktree handoff moved inside the exit branch for the same reason. It flips
// the flag that authorizes removing the run's worktree, and the FIXER's
// deliverable is an unpushed local commit by design — waiving that protection on
// an error the run then recovers from is how the commit would be lost.
func drivePRLoopExit(pmKey string, stage BoardStage, agentName, errorType string, advancePRLoop func(pmKey, agentName, errorType string) error, onHandoff func(agentName string), logger zerolog.Logger) bool {
	if stage != prReviewAgentStage && stage != prFixAgentStage {
		return false
	}
	if kind, reason := classifyErrorType(errorType); kind == endingPaused {
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Str("agent", agentName).
			Str("reason", reason).
			Msg("board PR loop: substrate failure mid-run, not treating it as the step's exit")
		return true
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
	// A well-formed block naming a stage the board cannot resume is a
	// configuration error, not a normal pause: it must never draw a *-failed
	// marker or a relaunch (that loop is exactly what hid the ticket-review gate's
	// decision), so treat it as a clean stop here — the card surfaces the error
	// through derivation instead (attachOpenOptions, SC-2137).
	if !optionStages[optStage] {
		return true
	}
	// A block offering too few answers is malformed for the same reason and takes
	// the same exit: attachOpenOptions reds the card with an explanation, and a
	// relaunch here would fight that with a re-run of the stage that posted it.
	if len(opts) < marker.MinDecisionOptions {
		return true
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
	if reachable != nil {
		switch r := reachable(branch); r.Status {
		case ProbeAbsent:
			// The branch is genuinely not on this machine — a board-context fix
			// leaves its branch local on the machine that produced it. Leave the
			// handoff for a daemon that can reach it (SC-652).
			logger.Debug().Str("pm", pmKey).Str("branch", branch).
				Msg("board chain: handoff branch not on this machine, leaving for a daemon that can reach it")
			return
		case ProbeUnreadable:
			// The reachability probe could not run (unresolvable dir, git error,
			// timeout). Do NOT strand the work silently: surface why and leave it to
			// be retried (SC-2403 sibling).
			postHandoffCheckUnreadable(ctx, pmKey, branch, r.Detail, commenter, daemonID, logger)
			return
		}
	}
	switch p := commitPresenceForHandoff(comments, branch, commitsPresent); p.Status {
	case ProbeAbsent:
		// A definite phantom-commit handoff (a retry that never pushed its work) —
		// the loud failure the ticket wants: red the card, do not review nothing.
		body := ImplementationFailedHeader +
			"\nhandoff names commits absent from branch " + branch + " on this machine — re-run the fix"
		if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
			logger.Warn().Err(err).Str("pm", pmKey).Msg("board chain: cannot post phantom-commit failure")
		}
		return
	case ProbeUnreadable:
		// The commit-presence check could not be performed. Never red good work on
		// a check that did not complete — surface which probe and why, and leave it
		// for reconcile to retry (SC-2403).
		postHandoffCheckUnreadable(ctx, pmKey, branch, p.Detail, commenter, daemonID, logger)
		return
	}
	if err := chainReview(pmKey); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Msg("board chain: cannot start review after build")
	}
}

// postHandoffCheckUnreadable surfaces WHICH board check could not be performed
// and why, without reddening the card. The card keeps its ready-for-review
// state, so the durable reconcile pass retries the check — an unreadable probe
// is left/retried, never treated as evidence of missing work (SC-2403).
func postHandoffCheckUnreadable(ctx context.Context, pmKey, branch, detail string, commenter tracker.Commenter, daemonID string, logger zerolog.Logger) {
	body := HandoffCheckUnreadableHeader +
		"\ncould not verify the handoff for branch " + branch + " on this machine — " + detail +
		"\nleaving the card for a daemon that can complete the check; it will be retried."
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Msg("board chain: cannot post handoff-check-unreadable diagnostic")
	}
}

// commitPresenceForHandoff reports the tri-state presence of the latest
// handoff's named commits on branch. A nil gate or a handoff naming no commits
// is ProbePresent (nothing to refute) — preserving the "no gate ⇒ proceed"
// default.
func commitPresenceForHandoff(comments []tracker.Comment, branch string, commitsPresent CommitsPresent) ProbeResult {
	if commitsPresent == nil {
		return ProbeResult{Status: ProbePresent}
	}
	commits := ParseCommitsFromHandoff(latestHandoffBody(comments))
	if len(commits) == 0 {
		return ProbeResult{Status: ProbePresent}
	}
	return commitsPresent(branch, commits)
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

// terminalStopVerdicts registers, per marker type, the head tokens that record a
// DELIBERATE, recorded decision to STOP the work — a gate concluding the ticket
// must not proceed. Recognized regardless of which stage classification the
// marker carries, because a gate may run under one stage yet file its verdict
// under another (the ticket-review gate runs under PLANNING but files
// [human:ticket-review] under BACKLOG, SC-1856). Scoping the clean-stop check to
// the running stage is exactly what let this defect class recur. New gates
// register one entry here and can never be mistaken for a crash by default.
//
// bug-verdict is deliberately absent: `confirmed` means proceed, and
// `not-a-bug`/`undetermined` are already terminated by the same-stage
// [human:no-fix-needed] marker. ready/reframed are absent for the same reason —
// they continue into planning.
var terminalStopVerdicts = map[string]map[string]bool{
	TicketReviewMarkerType: {"superseded": true, "escalated": true, "rejected": true},
}

// deliberateStopRecorded reports whether the thread's latest verdict of any
// registered gate records a deliberate stop. Stage-agnostic on purpose: it reads
// the verdict marker directly rather than through the stage-scoped
// latestStageState, so a gate that files its verdict under a stage other than
// the one it ran under is still recognized as a clean ending.
func deliberateStopRecorded(comments []tracker.Comment) bool {
	for markerType, stopHeads := range terminalStopVerdicts {
		if m, ok := marker.Latest(comments, markerType); ok && stopHeads[m.Head] {
			return true
		}
	}
	return false
}

// terminalResolutions registers the marker headers that record a FINAL
// DETERMINATION ABOUT THE TICKET rather than progress through a stage: the work
// is already merged (nothing-to-do), no code change is warranted
// (no-fix-needed), or the launch was refused for want of a plan
// (needs-planning). What makes them a class is that each files under a stage
// that ranks BELOW the phantom runs it supersedes, so furthest-stage-wins gives
// the wrong card unless the determination is recognized as terminal (SC-3555).
// Registering the fact here — beside terminalStopVerdicts, whose head tokens
// this set reuses rather than re-listing — means the next cross-stage terminal
// costs one line instead of re-discovering the same hole.
var terminalResolutions = []string{
	NothingToDoHeader,
	NoFixNeededHeader,
	NeedsPlanningHeader,
}

// isTerminalResolution reports whether a comment body is a registered terminal
// determination — either a whole-header resolution or a gate verdict whose head
// token records a deliberate stop (ticket-review rejected/superseded/escalated).
// The head is read through the marker grammar, never by slicing the first line,
// so a signed marker (machine:/build: spliced in) classifies exactly like an
// unsigned one.
func isTerminalResolution(body string) bool {
	trimmed := strings.TrimSpace(body)
	for _, header := range terminalResolutions {
		if strings.HasPrefix(trimmed, header) {
			return true
		}
	}
	m, ok := marker.ParseBody(trimmed)
	return ok && terminalStopVerdicts[m.Type][m.Head]
}

// stageSettled reports whether the stage's latest marker is one of the clean,
// non-failure endings handleBoardAgentExit treats as settled: the stage's own
// done-marker, a terminal BoardResolved marker, an open same-stage
// [human:options] pause, or a recorded deliberate stop verdict from any gate
// (deliberateStopRecorded, stage-agnostic). It is no longer the exact negation
// of the failure branch: for the implementation stage it ALSO keeps re-reading
// while the merged verification stage looks in-flight (SC-782's autofix
// container runs the review in-place and can post its
// [human:review-complete] handoff a moment after the implementation's own
// done-marker). Giving that second, later handoff the same settle-wait chance
// the first one gets closes the SC-2133 race where a clean exit's read lands
// between the two handoffs and misreads the still-propagating review as a
// mid-review death.
//
// A *-outage marker is deliberately NOT a settled state here: it is a transient
// the durable reconcile pass owns (it relaunches the stage each interval until
// the substrate returns, SC-2307), never a clean ending. An outage exit posts no
// marker of its own before this runs — handleBoardAgentExit posts the *-outage
// marker after routing on recordedOutage — so this only ever sees the stage's
// still-running *-started marker and returns unsettled after the bounded
// re-check budget, exactly as it would for any incomplete stage.
func stageSettled(comments []tracker.Comment, stage BoardStage) bool {
	_, state := latestStageState(comments, stage)
	if state != BoardDone && state != BoardResolved && !stagePausedOnOptions(comments, stage) && !deliberateStopRecorded(comments) {
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

// silenceReapSentinel is the fixed substring every silence-reap marker body
// carries. Pinned in code so a later reword of silenceReapReason can never
// silently break silenceReapCount, which counts prior reaps by matching it.
const silenceReapSentinel = "machine-chosen stop"

// silenceReapGiveUpSentinel is the fixed substring the give-up marker
// carries, pinned for the same reason and used by silenceReapGaveUp to dedup
// so two daemons reaching the cap at once do not both post it.
const silenceReapGiveUpSentinel = "stopped relaunching after"

// MaxSilenceReaps bounds how many times one stage may be silence-reaped and
// automatically relaunched before the machine stops trying and says plainly
// that a person is needed. Not charging a silence reap against the retry
// budget is right (SC-2447) — the work did not fail, a judgement about it
// did — but hiding a REPEATED misjudgement is not: after this many stops on
// one stage, repetition itself is the thing that needs a look (SC-3074).
const MaxSilenceReaps = 3

// silenceReapReason composes the plain, self-explaining line a silence-reap
// posts in place of the generic diagnosis: what the daemon observed, and
// exactly what rule it applied — a machine that reaps an agent for silence
// must not leave a reader guessing between a crash, a kill, and a restart
// (SC-2447's third and fourth wanted outcomes). "no outstanding model
// request" names the SC-3074 signal directly, replacing the disproven
// transcript-output heuristic in the sentence.
func silenceReapReason(idle string) string {
	return "the daemon observed " + idle + " with no sign of life — no tool activity and no outstanding " +
		"model request — past the idle budget, and stopped the stage. This is a " + silenceReapSentinel + ", not a stage " +
		"failure, so it is not charged against the retry budget. The stage was relaunched automatically."
}

// silenceReapGiveUpReason composes the line posted once a stage has been
// silence-reaped MaxSilenceReaps times: the machine stops relaunching and
// says a person is needed, naming how many times it happened so the pattern
// — not any single stop — is what the reader is asked to look at.
func silenceReapGiveUpReason(stage BoardStage, stops int) string {
	return fmt.Sprintf("the daemon %s %d silence reaps on the %s stage and is not relaunching again — this "+
		"needs a person. Each stop was a %s, not a stage failure, and none were charged against the retry "+
		"budget; the repetition itself is what needs a look.",
		silenceReapGiveUpSentinel, stops, stage, silenceReapSentinel)
}

// silenceReapCount counts prior silence-reap markers already posted under
// stage's failed header. Counted from the tracker thread itself, not a
// state-DB counter: state.db is per-host, so only the shared thread can dedup
// "two machines do not both post it" across every daemon that might reap this
// stage (SC-3074).
func silenceReapCount(comments []tracker.Comment, stage BoardStage) int {
	n := 0
	for _, c := range comments {
		s, st, ok := ClassifyMarker(c.Body)
		if !ok || s != stage || st != BoardFailed {
			continue
		}
		if strings.Contains(c.Body, silenceReapSentinel) {
			n++
		}
	}
	return n
}

// silenceReapGaveUp reports whether the give-up marker has already been
// posted for stage, so a second daemon reaching the cap for the same stage
// posts nothing more — stated once (SC-3074).
func silenceReapGaveUp(comments []tracker.Comment, stage BoardStage) bool {
	for _, c := range comments {
		s, st, ok := ClassifyMarker(c.Body)
		if !ok || s != stage || st != BoardFailed {
			continue
		}
		if strings.Contains(c.Body, silenceReapGiveUpSentinel) {
			return true
		}
	}
	return false
}

// appendModelOutcomeNote enriches a failure marker body with a one-line note
// naming the run's latest model-call outcome class when the boundary recorded a
// failing one — an auth lapse, a rate-limit, an overload, a spend-limit, a
// dropped connection. It is strictly additive: with no lookup wired, no recorded
// outcome for the run, or a healthy last call (ClassOK), it returns the body
// unchanged so today's marker is preserved byte-for-byte (SC-2555 step 5b). The
// note lands in the detail block, never on the headline line the card badge
// reads (failureReason), so the badge's meaning is unchanged too.
func appendModelOutcomeNote(body string, latest LatestOutcomeClass, ticket, stage string) string {
	if latest == nil {
		return body
	}
	class, ok := latest(ticket, stage)
	if !ok || class == "" || class == proxy.ClassOK {
		return body
	}
	return body + "\n\n" + modelOutcomeReason(class)
}

// modelOutcomeReason renders a boundary outcome class as the human line the
// failure marker's detail pane carries, so a reader learns why a run failed
// from the ticket alone rather than from agent logs.
func modelOutcomeReason(class string) string {
	return "last model-API call at the network boundary was recorded as \"" + class + "\"."
}

// needsPersonReason composes the deploy-style situation+next-action line for a
// wall that does not self-heal: the card reads what is wrong and the one
// gesture that resolves it, never an exit code or a guessed-at cause. reason
// is the substrate phrase classifyUnavailability names (e.g. "model API
// authentication was refused"); a spend/credit reason gets its own next
// action, everything else defaults to re-authenticating.
func needsPersonReason(reason string) string {
	action := "re-authenticate the model API on the daemon host, then re-run this stage."
	if strings.Contains(strings.ToLower(reason), "billing") || strings.Contains(strings.ToLower(reason), "credit") {
		action = "restore the model API billing/credit, then re-run this stage."
	}
	if reason == "" {
		reason = "the model API refused the run"
	}
	return "paused for a person — " + reason + "; " + action
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
