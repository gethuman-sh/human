package daemon

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/marker"
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
	// Assignee and Reporter answer whose ticket this is, the fact the work gate
	// needs to refuse another person's work (SC-4063). Empty on both means the
	// owner could not be resolved, which the gate reads as unknown rather than as
	// someone else's — see WorkGate.mineToWork.
	Assignee string
	Reporter string
}

// ReconcileLister enumerates the open PM cards to reconcile. Injected so the
// enumeration (tracker fan-out) stays in the command layer and the pass itself
// stays pure and testable.
type ReconcileLister func(ctx context.Context) ([]ReconcileCard, error)

// ProbeStatus is the tri-state a board git probe now answers with: the fact was
// read as present, read as a clean absence, or could not be read at all (an
// unresolvable project dir, a git error, or a probe past its timeout). Widening
// the probe result from a bare bool is what lets the live chain stop reddening a
// card on a check that never completed (SC-2403).
type ProbeStatus int

const (
	ProbeUnreadable ProbeStatus = iota // zero value: the safe default that never reds a card
	ProbePresent
	ProbeAbsent
)

// ProbeResult carries a probe's tri-state outcome and, when Unreadable, a
// human-readable reason (which probe and why, plus a remedy) for the card.
type ProbeResult struct {
	Status ProbeStatus
	Detail string // populated only for ProbeUnreadable
}

// BranchReachable reports whether a handoff branch resolves on THIS machine —
// as a local ref or on origin. A board-context fix leaves its branch local on
// the machine that produced it, so a daemon on another machine cannot serve a
// review for it; gating the review chain on reachability leaves such a handoff
// for a daemon that can reach the branch. A nil predicate disables the gate
// (every branch treated as reachable), matching the package's "nil disables"
// convention for optional deps.
type BranchReachable func(branch string) ProbeResult

// CommitsPresent reports whether every named commit is reachable from branch on
// THIS machine (local ref or origin/<branch>). It layers on BranchReachable: a
// handoff must not merely name a branch this machine can resolve, but a branch
// that actually CONTAINS the commits it binds a review/deploy against — a
// retry's handoff naming SHAs that were never pushed anywhere is the failure it
// guards (735). A nil predicate disables the gate, matching the package's "nil
// disables" convention for optional deps.
type CommitsPresent func(branch string, commits []string) ProbeResult

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
func RunBoardReconcile(ctx context.Context, listCards ReconcileLister, reachable BranchReachable, participates ProjectParticipation, identityFor TicketIdentity, commitsPresent CommitsPresent, mergedProbe PRMergedProbe, postDeployed DeployedPoster, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, closedProbe ClosedTicketProbe, chainReview func(pmKey string) error, driveLoop func(pmKey string) error, retry StageRetry, progress AgentProgressProbe, stopAgent func(agentName string) error, daemonID string, interval time.Duration, logger zerolog.Logger) {
	if listCards == nil || chainReview == nil {
		return
	}

	logger.Info().Msg("board reconcile started")

	gate := WorkGate{reachable: reachable, participates: participates, daemonID: daemonID, identityFor: identityFor}

	// Recover a restart-orphaned handoff immediately, before the first wait. The
	// jitter applies only to subsequent cycles, so a restart-orphan is never made
	// to wait a full interval.
	reconcileOnce(ctx, listCards, gate, commitsPresent, mergedProbe, postDeployed, liveAgents, postFailed, closedProbe, chainReview, driveLoop, retry, progress, stopAgent, daemonID, logger)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(JitteredInterval(interval, BoardReconcileJitter)):
			reconcileOnce(ctx, listCards, gate, commitsPresent, mergedProbe, postDeployed, liveAgents, postFailed, closedProbe, chainReview, driveLoop, retry, progress, stopAgent, daemonID, logger)
		}
	}
}

// JitteredInterval returns d randomly perturbed by up to ±d*fraction, floored
// at zero, so N independently started daemons spread their reconcile wake-ups
// instead of firing on the same wall-clock tick. A non-positive fraction
// returns d unchanged.
func JitteredInterval(d time.Duration, fraction float64) time.Duration {
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
func reconcileOnce(ctx context.Context, listCards ReconcileLister, gate WorkGate, commitsPresent CommitsPresent, mergedProbe PRMergedProbe, postDeployed DeployedPoster, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, closedProbe ClosedTicketProbe, chainReview func(pmKey string) error, driveLoop func(pmKey string) error, retry StageRetry, progress AgentProgressProbe, stopAgent func(agentName string) error, daemonID string, logger zerolog.Logger) {
	cards, err := listCards(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("board reconcile: cannot list PM cards")
		return
	}
	// The choke point (SC-2047, widened by SC-4063): every pass below that WRITES
	// to a ticket is handed cards only through the gate — forReview for the ones
	// that continue a finished-and-handed-off stage, forTakeover for those that red
	// and relaunch a still-running stage, forOwnWork for the one whose subject is
	// the forge rather than a branch. So no pass can act on a ticket belonging to
	// another person, or on work this machine cannot reach or does not own.
	//
	// reconcileOrphanedAgents is the ONE pass left on the raw list, and only
	// because it writes to no ticket: it stops containers running on THIS machine,
	// which is this machine's business whoever owns the ticket — leaving a
	// container alive because its ticket is someone else's would strand a process
	// nobody else can reach.
	if n := reconcileOrphanedHandoffs(gate.forReview(cards), commitsPresent, chainReview, logger); n > 0 {
		logger.Info().Int("launched", n).Msg("board reconcile: chained review for orphaned handoffs")
	}
	if n := reconcileShippedFailures(ctx, gate.forOwnWork(cards), mergedProbe, postDeployed, logger); n > 0 {
		logger.Info().Int("cleared", n).Msg("board reconcile: confirmed shipped, cleared stale deploy-failed red")
	}
	// The PR-loop re-drive runs BEFORE the stuck-running pass so a loop card
	// stranded by a restart is re-driven rather than reddened — the stuck pass
	// also skips it (doneStageLoopActive), but ordering makes the ownership clear.
	if n := reconcilePRLoops(ctx, gate.forTakeover(cards), liveAgents, driveLoop, logger); n > 0 {
		logger.Info().Int("redriven", n).Msg("board reconcile: re-drove stalled PR review→fix loops")
	}
	// An outage card is re-driven BEFORE the stuck-running pass: it is not a hang
	// to be reddened but a stage waiting on the substrate, relaunched uncharged
	// each tick (the backoff) until the substrate returns (SC-2307). It rides the
	// same forTakeover gate — an outage relaunch takes over the stage on this
	// machine exactly as a stuck-running reclaim does.
	if redriven, handedOver := reconcileOutage(ctx, gate.forTakeover(cards), liveAgents, postFailed, retry, daemonID, time.Now(), logger); redriven > 0 || handedOver > 0 {
		logger.Info().Int("redriven", redriven).Int("handed_over", handedOver).
			Msg("board reconcile: re-drove stages waiting on the substrate")
	}
	if n := reconcileStuckRunning(ctx, gate.forTakeover(cards), liveAgents, postFailed, retry, progress, stopAgent, daemonID, time.Now(), logger); n > 0 {
		logger.Info().Int("reddened", n).Msg("board reconcile: reddened stuck-running cards with no live agent")
	}
	// After the stuck pass, because a queued card is the one thing that pass is
	// built NOT to touch: BoardQueued exists so a just-decided card is not redded
	// (SC-1320), which left it watched by nothing at all (SC-3865). It takes the
	// same forTakeover gate as the other relaunching passes — starting the stage a
	// decision queued takes that stage over on this machine.
	if n := reconcileQueuedLaunch(ctx, gate.forTakeover(cards), liveAgents, retry, daemonID, time.Now(), logger); n > 0 {
		logger.Info().Int("launched", n).Msg("board reconcile: started stages left queued by an answered decision")
	}
	// Last: the passes above all act on cards that are still ON the board, while
	// this one reaches the runs whose card has left it — the orphan the close
	// gate cannot cover because the ticket was closed outside the board (1698).
	if n := reconcileOrphanedAgents(ctx, cards, liveAgents, closedProbe, stopAgent, postFailed, logger); n > 0 {
		logger.Info().Int("stopped", n).Msg("board reconcile: stopped agents orphaned on closed tickets")
	}
}

// stageStalled reports whether a live agent has stopped making progress.
//
// An unknown agent (no probe, or a daemon that restarted and lost its progress
// map) is NOT treated as stalled: killing live work on absent evidence is the
// one failure this must never risk, and the container-liveness check has
// already established something is running.
func stageStalled(progress AgentProgressProbe, agentName string, now time.Time) (bool, time.Duration) {
	if progress == nil {
		return false, 0
	}
	p, ok := progress(agentName)
	if !ok {
		return false, 0
	}
	return p.Stalled(now)
}

// doneStageLoopActive reports whether the card's newest done-stage marker is a
// PR-loop started marker — the review→fix loop is mid-flight rather than a plain
// deploy. Used to hand loop cards to the re-drive pass, to keep the generic
// stuck-running pass from redding them, and (board_state.go) to badge the loop.
func doneStageLoopActive(comments []tracker.Comment) bool {
	return doneStageLoopHalf(comments) != ""
}

// doneStageLoopHalf names WHICH half of the review→fix loop the card's newest
// done-stage marker started: "pr-review", "pr-fix", or "" when the newest marker
// is not a loop-started marker at all.
//
// The distinction exists everywhere else — prReviewAgentStage and
// prFixAgentStage are separate agents in separate containers — and the badge
// alone collapsed it, so a card whose live container was -prfix running the PR
// fixer read "PR review…" for the whole loop (SC-4151 F15).
func doneStageLoopHalf(comments []tracker.Comment) string {
	_, latest := latestStateInStage(comments, BoardDoneStage)
	t := strings.TrimSpace(latest.Body)
	switch {
	case strings.HasPrefix(t, PRReviewStartedHeader):
		return DeployPhasePRReview
	case strings.HasPrefix(t, PRFixStartedHeader):
		return DeployPhasePRFix
	default:
		return ""
	}
}

// reconcilePRLoops re-drives a loop card the live exit hook missed: a
// done/running card whose newest done marker is a loop-started marker and for
// which no loop half-agent is alive on this machine (a daemon restart lost the
// Stop event). driveLoop re-reads the recorded state and advances or escalates,
// idempotently (AdvancePRLoop's escalate no-ops on an already-open options
// block, and the alive-guard prevents racing a second launch). nil deps disable it.
//
// It receives DrivableCards from the forTAKEOVER gate, not forReview. A
// mid-flight review→fix loop is a RUNNING stage, and the gate's two intents split
// exactly there: forTakeover governs a still-running stage, forReview governs work
// its owner has finished and handed off. Being on the wrong side of that line is
// what let a peer daemon red a review it was not running (SC-4025).
//
// The peer cannot help itself. The outcome this pass reads — the reviewer's
// verdict in stage.pr-review — lives in the agent state store, which is local to
// the daemon host and never posted to the tracker. A machine that did not launch
// the reviewer reads its own empty store, concludes the step recorded nothing, and
// escalates: on SC-3613 and SC-3569 that happened 21 to 117 seconds into rounds
// that went on to finish and approve. Reachability could not have stopped it,
// because the loop pushes its branch before opening the draft PR, so origin
// resolves the branch on every peer.
//
// forTakeover carries the reachability arm too, so the SC-2047 property this pass
// had before is unchanged; what it adds is the ownership arm, and the loop's
// started marker carries the machine: stamp that arm reads. The daemon id is
// persisted across restarts (LoadOrCreateDaemonID), so the restart-orphan case
// this pass exists for still matches its own stamp and is still re-driven.
func reconcilePRLoops(ctx context.Context, drivable DrivableCards, liveAgents LiveAgentLister, driveLoop func(pmKey string) error, logger zerolog.Logger) int {
	if driveLoop == nil || liveAgents == nil {
		return 0
	}
	names, err := liveAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("board reconcile: cannot list live agents for PR loops")
		return 0
	}
	alive := make(map[string]struct{}, len(names))
	for _, n := range names {
		alive[n] = struct{}{}
	}
	redriven := 0
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.Stage != BoardDoneStage || derived.State != BoardRunning {
			continue
		}
		if !doneStageLoopActive(card.Comments) {
			continue
		}
		// A live loop half-agent owns the card — leave it; re-driving would race a
		// second launch onto the same step.
		if _, ok := alive[agentNameFor(card.Key, prReviewAgentStage)]; ok {
			continue
		}
		if _, ok := alive[agentNameFor(card.Key, prFixAgentStage)]; ok {
			continue
		}
		if err := driveLoop(card.Key); err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).Msg("board reconcile: cannot re-drive PR loop")
			continue
		}
		redriven++
	}
	return redriven
}

// reconcileOutage relaunches a card that recorded an outage (ExitOutage) and
// whose stage agent is not alive on this machine. This is the backoff: each
// reconcile tick re-drives it (retry.tryRelaunch classifies the recorded outage
// and relaunches WITHOUT charging DefaultStageRetries) until the substrate
// returns and the relaunched agent posts a *-started marker that supersedes the
// outage. Free in attempts by design — an outage costs time and nothing else
// (SC-2307).
//
// Not free in time, though: a wait past OutageWaitBound is handed to a person
// instead of relaunched, because an outage that never ends looks exactly like
// one that will return until you measure how long it has lasted, and nobody was
// ever told the difference (SC-2851). The handover reds the card and still
// charges nothing.
//
// A live agent for the stage means the relaunch already happened this cycle, so
// the card is left alone rather than racing a second launch onto the same stage
// — the same alive-guard reconcilePRLoops and reconcileStuckRunning use. nil
// deps or an unwired retry disable the pass (the package's "nil disables"
// convention); an unwired postFailed disables only the handover, leaving the
// indefinite wait rather than stranding the card with neither.
//
// Returns how many cards were re-driven and how many were handed to a human.
func reconcileOutage(ctx context.Context, drivable DrivableCards, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, retry StageRetry, daemonID string, now time.Time, logger zerolog.Logger) (redriven, handedOver int) {
	if liveAgents == nil || !retry.enabled() {
		return 0, 0
	}
	names, err := liveAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("board reconcile: cannot list live agents for outage re-drive")
		return 0, 0
	}
	alive := make(map[string]struct{}, len(names))
	for _, n := range names {
		alive[n] = struct{}{}
	}
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.State != BoardOutage {
			continue
		}
		// A live agent means the relaunch already happened this cycle — leave it.
		if _, ok := alive[agentNameFor(card.Key, derived.Stage)]; ok {
			continue
		}
		if since, ok := outageRunSince(card.Comments, derived.Stage); ok && postFailed != nil && now.Sub(since) > OutageWaitBound {
			if handOverOutage(ctx, card.Key, derived, postFailed, now.Sub(since), since, daemonID, logger) {
				handedOver++
			}
			continue
		}
		// The card already derived to BoardOutage from its standing *-outage
		// marker, so relaunch through the uncharged path directly rather than
		// retry.tryRelaunch: that path classifies from retry.Outcome, which reads
		// ("", false) for a card whose outage was recognised from a signal alone
		// (the SC-2856 refusal, never recorded via the retry policy) — and
		// misclassifies an unrecorded outcome as relaunchBounded, charging the
		// very budget an outage must never spend (SC-3024).
		if resume, ok := parseResume(derived); ok && now.Before(resume) {
			// Still inside the stated wait — do not relaunch this tick.
			continue
		}
		if retry.relaunchOutage(card.Key, derived.Stage, logger) {
			redriven++
		}
	}
	return redriven, handedOver
}

// parseResume parses a derived card's ResumeAt (an RFC3339 instant a paused
// outage's standing marker stated, when one was parsed out of the diagnosis).
// ok is false for an absent or unparseable value, in which case the caller
// falls back to its existing per-tick backoff exactly as before.
func parseResume(derived BoardCard) (time.Time, bool) {
	if derived.ResumeAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, derived.ResumeAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// stuckRunningCandidate reports whether a card is eligible for the stuck-running
// red: it is in a running state AND not a mid-flight PR review→fix loop (which
// reconcilePRLoops owns). A loop card's half-agents come and go between rounds,
// so this pass must never treat the gap as a hang.
func stuckRunningCandidate(derived BoardCard, comments []tracker.Comment) bool {
	return derived.State == BoardRunning && !doneStageLoopActive(comments)
}

// reconcileStuckRunning reds the dead-end a NOT DONE bug-verify (and any other
// silently-halted stage) leaves behind: a card frozen in a running state with
// no terminal marker and no live agent. The live exit-hook watcher and the
// container-only zombie sweep both miss it on a daemon restart or a dropped
// hook, so the card sits at "being fixed" forever (1136). This is the durable
// safety net — the bug-fix analog of the no-dead-end-states work (SC-355/591).
//
// A card is reddened only when ALL hold: its derived state is BoardRunning; it
// carries no open [human:options] block for its OWN stage (that is a
// deliberate human pause, not a hang — the durable twin of the live path's
// stagePausedOnOptions guard); its stage has a *-failed marker
// (Planning/Implementation/Verification/Done); it has sat past
// StuckRunningGrace; and no board agent for (key, stage) is alive on this
// machine. The grace plus the liveness probe spare a genuinely slow but live
// run — only a card with no owner is failed. Nil deps or a lister error do
// nothing: the pass never reds a card it cannot prove is dead. Idempotent —
// once the *-failed marker lands the card derives BoardFailed, so the next tick
// skips it and never double-posts. Reuses DeriveBoardCard verbatim so detection
// can never disagree with the board's rendered state. Returns the number reddened.
//
// It receives DrivableCards from the forTakeover gate, so every card it sees is
// already this machine's to red: the project is one it participates in, the stage
// is not owned by a peer daemon, and the branch (if any) resolves here. That is
// why this pass no longer weighs a foreign grace or a reachability predicate — the
// single local StuckRunningGrace with real machine-local liveness evidence is
// correct for every card that reaches it, because a card owned elsewhere never
// does (SC-2047: ownership binds a running stage to its machine, so the
// delay-only StuckRunningForeignGrace is retired rather than lengthened).
// hungLiveAgent decides, for a card whose stage agent is still alive, whether
// that agent has stopped making progress and, if so, stops it before anything
// relaunches. It reports whether the caller should proceed to red the card
// (false covers both "genuinely working" and "could not be stopped" — neither
// is evidence the card is dead) and, when proceeding, whether the stop was
// this pass's own judgement (silenced) plus the idle duration to report.
// Pulled out of reconcileStuckRunning to keep that function's branching
// inside the complexity gate.
func hungLiveAgent(progress AgentProgressProbe, agentName string, now time.Time, stopAgent func(agentName string) error, pmKey string, stage BoardStage, logger zerolog.Logger) (proceed, silenced bool, idleReason string) {
	// A live container is not the same as a working agent: a hung agent looks
	// perfectly healthy here, which is why a hang was previously never
	// detected at all. Ask whether it is still making progress.
	stalled, idle := stageStalled(progress, agentName, now)
	if !stalled {
		return false, false, "" // genuinely working, however long it has been running
	}
	logger.Warn().Str("pm", pmKey).Str("stage", string(stage)).
		Dur("idle", idle).Msg("board reconcile: agent alive but making no progress, treating as hung")
	// A hung agent still holds its container and workspace, so it must be
	// stopped before anything relaunches — otherwise two agents work the same
	// stage. A stop that fails (or is unwired) leaves the card alone rather
	// than risking that.
	if stopAgent == nil {
		return false, false, ""
	}
	if err := stopAgent(agentName); err != nil {
		logger.Warn().Err(err).Str("agent", agentName).
			Msg("board reconcile: cannot stop hung agent, leaving the card as-is")
		return false, false, ""
	}
	return true, true, idle.Round(time.Second).String()
}

func reconcileStuckRunning(ctx context.Context, drivable DrivableCards, liveAgents LiveAgentLister, postFailed FailedMarkerPoster, retry StageRetry, progress AgentProgressProbe, stopAgent func(agentName string) error, daemonID string, now time.Time, logger zerolog.Logger) int {
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
	for _, card := range drivable.cards {
		if reconcileOneStuckCard(ctx, card, alive, postFailed, retry, progress, stopAgent, daemonID, now, logger) {
			reddened++
		}
	}
	return reddened
}

// reconcileOneStuckCard applies the stuck-running judgement to a single card
// and reports whether it was reddened. Split out of reconcileStuckRunning so
// that function's per-card branching costs its own function rather than the
// loop's complexity budget.
func reconcileOneStuckCard(ctx context.Context, card ReconcileCard, alive map[string]struct{}, postFailed FailedMarkerPoster, retry StageRetry, progress AgentProgressProbe, stopAgent func(agentName string) error, daemonID string, now time.Time, logger zerolog.Logger) bool {
	derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
	if !stuckCardIsOursToRed(derived, card) {
		return false
	}
	failedType := failedTypeFor(derived.Stage)
	if failedType == "" {
		return false
	}
	if now.Sub(derived.StageEnteredAt) < StuckRunningGrace {
		// Young enough to still be genuine in-flight work.
		return false
	}
	agentName := agentNameFor(card.Key, derived.Stage)
	// silenced marks a stop THIS pass chose because a live agent stopped
	// making progress — a machine-chosen stop, not a stage failure, so it
	// must not consume the ticket's retry budget (SC-2447). A vanished
	// agent (no entry in alive) is a genuine, unexplained death and stays
	// on the charged path unchanged.
	var silenced bool
	var idleReason string
	if _, ok := alive[agentName]; ok {
		proceed, s, reason := hungLiveAgent(progress, agentName, now, stopAgent, card.Key, derived.Stage, logger)
		if !proceed {
			return false
		}
		silenced, idleReason = s, reason
	}
	// Repeated silence reaps are bounded and visible, identically to the live
	// failure watcher's cap (SC-3074): at or over MaxSilenceReaps this stops
	// relaunching and says a person is needed, naming the count once; skip
	// is set when the give-up marker is already on the thread, so a second
	// daemon reaching the same cap posts nothing more.
	failed, givingUp, skip := stuckRunningSilenceBody(failedType, derived.Stage, card.Comments, silenced, idleReason)
	if skip {
		return false
	}
	// If this stage was preceded by a recorded inter-stage wait, name that
	// cause in the red so a stall that followed a long wait is attributable
	// rather than judged from silence (SC-2462). It joins the detail prose, not
	// the field block: it qualifies the diagnosis rather than being read back.
	if cause := latestStageWaitCause(card.Comments); cause != "" {
		failed.Body = strings.TrimSpace(failed.Body + "\nafter wait cause: " + cause)
	}
	if err := postFailed(ctx, card.Key, markerBody(failed)); err != nil {
		logger.Warn().Err(err).Str("pm", card.Key).
			Msg("board reconcile: cannot red stuck-running card")
		return false
	}
	if silenced {
		if !givingUp {
			// A live agent this pass judged hung and stopped itself: uncharged,
			// like the live failure watcher's silence-reap path (SC-2447) — the
			// work did not fail, a judgement about the work did.
			retry.relaunchSilenceReap(card.Key, derived.Stage, logger)
		}
		// givingUp: the cap is spent, leave the card as the give-up marker just
		// routed it, for a person to look at — no further relaunch.
		return true
	}
	// This is the fallback path the live failure watcher misses — an agent
	// that died with no exit hook (a daemon restart, a dropped event). A
	// vanished agent IS a real, unexplained death, so it stays on the
	// charged path: the same bounded relaunch applies, so a silently-dead
	// stage recovers here too instead of only reddening. The just-posted
	// failed marker is the trail record, so no separate retry note (nil
	// commenter); the shared per-stage budget bounds this path and the
	// watcher's together.
	retry.tryRelaunch(ctx, card.Key, derived.Stage, nil, daemonID, logger)
	return true
}

// stuckRunningSilenceBody composes the reddening body for a stuck-running
// card and reports whether the SC-3074 cap means this pass gives up rather
// than relaunches (givingUp), and whether the card should be skipped
// entirely because the give-up marker was already posted for this stage
// (skip — the dedup that keeps two daemons from both posting it). Split out
// of reconcileStuckRunning so that function's branching stays inside the
// complexity gate.
func stuckRunningSilenceBody(failedType string, stage BoardStage, comments []tracker.Comment, silenced bool, idleReason string) (m marker.Marker, givingUp, skip bool) {
	if !silenced {
		return failureMarker(failedType, stuckRunningReason(stage)), false, false
	}
	if silenceReapGaveUp(comments, stage) {
		return marker.Marker{}, false, true
	}
	stops := silenceReapCount(comments, stage) + 1
	if stops > MaxSilenceReaps {
		return failureMarker(failedType, silenceReapGiveUpReason(stage, stops)), true, false
	}
	return failureMarker(failedType, silenceReapReason(idleReason)), false, false
}

// cardPausedOnOpenOptions reports whether a card carries an open
// [human:options] block naming its own running stage or an EARLIER stage that
// answering the question would rework — the durable reconcile pass's twin of
// the live path's stagePausedOnOptions guard, expressed over the
// already-derived board card rather than raw comments (1290, generalized to
// rework questions by SC-1957).
func cardPausedOnOpenOptions(derived BoardCard) bool {
	return len(derived.Options) > 0 && stageRank[derived.OptionsStage] <= stageRank[derived.Stage]
}

// stuckCardIsOursToRed collects the two reasons a card that already reached this
// pass must STILL be left alone before any hang judgement is made: it is not a
// stuck-running candidate, or it is deliberately paused on a human decision. The
// third historical reason — the card's work lives on another machine — is no
// longer weighed here: it is now enforced upstream by the forTakeover gate, which
// never hands this pass a card owned elsewhere or a branch it cannot reach
// (SC-2047). Kept together so the "leave it alone" cases read as one unit.
func stuckCardIsOursToRed(derived BoardCard, card ReconcileCard) bool {
	// Only a running card with no active PR loop is a stuck-running candidate.
	// A mid-flight review→fix loop is owned by reconcilePRLoops, not this hang
	// detector: its half-agents come and go between rounds, so the absence of a
	// live agent here is normal rather than a dead-end.
	if !stuckRunningCandidate(derived, card.Comments) {
		return false
	}
	// An open [human:options] block naming the card's own running stage, or an
	// earlier stage the answer would rework, is a deliberate human pause, not a
	// hang — the live failure path already treats it as a clean pause
	// (stagePausedOnOptions). [human:options] is not a state marker, so the card
	// stays BoardRunning; without this twin guard the durable reconcile pass
	// reddens the pause and loops re-planning forever (1290, the planning twin
	// of SC-751; generalized to late-stage rework questions by SC-1957).
	if cardPausedOnOpenOptions(derived) {
		return false
	}
	// A gate that recorded a deliberate stop verdict (superseded/escalated/
	// rejected) ended the work on purpose — the durable twin of the live path's
	// deliberateStopRecorded guard, which handleBoardAgentExit and stageSettled
	// have honoured since SC-2302 while this pass did not. That asymmetry is what
	// let the class recur: whenever the live watcher misses the exit (a daemon
	// restart, a dropped hook, a machine powered off) the card falls through to
	// here, is redded as a hang, and is relaunched to reach the same verdict
	// again — SC-3149 twelve times in one night. Read the same way on both paths,
	// so the two can never disagree about what an ending is.
	if deliberateStopRecorded(card.Comments) {
		return false
	}
	return true
}

// branchActionableHere reports whether THIS machine could actually act on the
// card, by the only test that is a fact rather than a judgement: can it resolve
// the card's branch.
//
// A card with no branch yet — planning, or a build that has not handed off — is
// treated as actionable, because there is no fact to consult and refusing would
// disable the hang detector for every early stage. Those cards keep the older,
// softer protection (the grace plus the liveness probe). A nil predicate
// disables the gate, matching the package's "nil disables" convention.
func branchActionableHere(derived BoardCard, reachable BranchReachable) bool {
	if reachable == nil || derived.Branch == "" {
		return true
	}
	return reachable(derived.Branch).Status == ProbePresent
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
//
// It takes the forOwnWork gate rather than the raw board: posting [human:deployed]
// is a write on someone's ticket and must obey the ownership rule like every other
// write (SC-4063). It must NOT take forReview — that arm demands a reachable
// branch, and the branch of a merged PR is deleted at merge, so reachability would
// filter out exactly the cards this pass exists to clear.
func reconcileShippedFailures(ctx context.Context, drivable DrivableCards, mergedProbe PRMergedProbe, postDeployed DeployedPoster, logger zerolog.Logger) int {
	if mergedProbe == nil || postDeployed == nil {
		return 0
	}
	cleared := 0
	for _, card := range drivable.cards {
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
// The reachability gate guarding the chain now lives upstream: this pass receives
// DrivableCards from the forReview gate, so a review is chained only for a handoff
// whose branch this machine can resolve (local ref or on origin). A board-context
// fix leaves its branch local on the machine that produced it, so a daemon on
// another machine is never handed that card and leaves it for one that can reach
// the branch — never starting a review it could never satisfy (SC-652, now
// enforced by construction rather than a per-path check, SC-2047). Returns the
// number of reviews launched.
func reconcileOrphanedHandoffs(drivable DrivableCards, commitsPresent CommitsPresent, chainReview func(pmKey string) error, logger zerolog.Logger) int {
	launched := 0
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.Stage != BoardImplementation || derived.State != BoardDone {
			continue
		}
		// Skip-and-leave when the commits are not DEFINITELY present — a clean
		// absence (a retry that never pushed) or an unreadable check (this machine
		// cannot reach the repo). A periodic scan must never red a card another
		// machine can serve; the loud failure lives on the live chain, and only on
		// a definite absence (SC-2403).
		if commitPresenceForHandoff(card.Comments, derived.Branch, commitsPresent).Status != ProbePresent {
			logger.Warn().Str("pm", card.Key).Str("branch", derived.Branch).
				Msg("board reconcile: handoff commits not verifiable on this machine, leaving it")
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
