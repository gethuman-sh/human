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

// PRShippableProbe reports whether the pull request at prURL is DEFINITELY
// ready to ship: the forge reports it mergeable AND every check it reports has
// passed. Distinct from PRMergedProbe (already landed) and from MergeReader's
// bare mergeability — a merge verdict says nothing about CI, and the red this
// answers is a CI red. An error means the state could not be read, and a caller
// must leave the card exactly as it is: a recovery that fires on an unknown
// state is the failure the !merged rule has always guarded against (SC-3640).
// A nil probe disables the shippable arm ("nil disables").
//
// headSHA is the PR's current head commit, returned alongside the verdict so a
// caller can pace re-drives on the head rather than on the failure that
// prompted the check: the head is what "the pull request turned green" is
// actually about, and it is the one signal that does not rotate merely because
// a re-drive that started ended in another failure (SC-3640 round 2).
type PRShippableProbe func(ctx context.Context, prURL string) (shippable bool, headSHA string, err error)

// DeployRedrive re-drives the done stage for a card whose deploy failed — the
// same in-place retry transition the board's "Retry deploy" gesture issues, so
// every guard on that path (the idempotency drop, the open-decision refusal,
// the draft interlock, the PR review that fronts the merge) applies unchanged.
// It reports whether a re-drive actually STARTED, so a refusal that started
// nothing is never logged or paced as though it had. A nil value disables the
// shippable arm.
type DeployRedrive func(pmKey string) (launched bool, err error)

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
//
// Two bodies it carries are deliberately not failures — the run-cancelled
// record (board_reconcile_orphan.go) and the completed planning handoff
// (SC-5090). The name records the ordinary case, not a restriction.
type FailedMarkerPoster func(ctx context.Context, pmKey, body string) error

// ChainReview starts the review stage for a card whose build finished and
// handed off — the live chain's continuation and the durable pass's recovery of
// it. A nil value disables chaining.
//
// It, DriveLoop and AdvanceDeployFix are three DIFFERENT actions that share one
// underlying signature, and they sat as bare `func(pmKey string) error`
// parameters in adjacent positions: RunBoardReconcile took chainReview and
// driveLoop next to each other, so swapping the two arguments compiled and
// reviewed clean. Named types make the mistake unspellable at the call site.
type ChainReview func(pmKey string) error

// DriveLoop re-drives the PR review→fix loop from its recorded state, advancing
// or escalating it idempotently. A nil value disables the re-drive pass.
type DriveLoop func(pmKey string) error

// StopAgent stops a board agent's container on THIS machine by name. A nil
// value disables every pass that would kill a run.
type StopAgent func(agentName string) error

// StoppedAgentLister reports the board agents whose record on THIS machine says
// they have stopped, keyed by agent name, with the moment the stop was
// recorded. Where a stop IS recorded, the stuck-running pass otherwise never
// sees it, because a stopped agent simply leaves the live listing and the card
// then waits out the full StuckRunningGrace for a fact the machine already
// held (SC-5327). The production lister (agent.StoppedBoardAgents) reads
// two sources to cover every path an agent stops through, including the
// kill/OOM/crash reap the meta alone cannot show once DeleteMeta erases it —
// see its doc comment for which producer feeds which case. A nil lister
// disables the shortcut and every card keeps the grace.
type StoppedAgentLister func() (map[string]time.Time, error)

// ReconcileDeps wires the durable reconcile pass's collaborators, mirroring
// BoardTransitionDeps and FailureDeps in the same package. Every field follows
// the "nil disables" convention.
//
// It exists because these values were threaded positionally through
// RunBoardReconcile, reconcileOnce and every pass below them, and two of them —
// ChainReview and DriveLoop — are adjacent parameters of what used to be the
// same bare type. Naming the types made the swap unspellable; the struct means
// adding a collaborator is one field rather than an edit to every signature
// between the wiring and the pass that needs it.
type ReconcileDeps struct {
	ListCards      ReconcileLister
	Reachable      BranchReachable
	Participates   ProjectParticipation
	IdentityFor    TicketIdentity
	CommitsPresent CommitsPresent
	MergedProbe    PRMergedProbe
	// ShippableProbe and RetryDeploy are the second half of the done stage's own
	// recovery: a red card whose PR is not merged but has become mergeable with
	// every check green is re-driven rather than left for a person to click
	// (SC-3640). Both nil keeps the pass exactly as it was — merged-only.
	ShippableProbe PRShippableProbe
	RetryDeploy    DeployRedrive
	PostDeployed   DeployedPoster
	LiveAgents     LiveAgentLister
	StoppedAgents  StoppedAgentLister
	PostFailed     FailedMarkerPoster
	ClosedProbe    ClosedTicketProbe
	ChainReview    ChainReview
	DriveLoop      DriveLoop
	Retry          StageRetry
	Progress       AgentProgressProbe
	StopAgent      StopAgent
	// DeployRun tells the sweep when the deploy engine's own clock started for a
	// card, so a deploy waiting its turn at the unbounded deployGate is judged by
	// the engine rather than by a marker posted before it ever queued (SC-4150).
	// Nil — or an answer of !ok — means this machine knows nothing about the run,
	// and the marker clock is used exactly as before.
	DeployRun DeployRunProbe
	// DaemonID is this machine's identity: it tells this daemon's own running
	// stage from a peer's, and signs every marker the passes post.
	DaemonID string
	// Interval is how long to wait between passes after the immediate one at start.
	Interval time.Duration
	Logger   zerolog.Logger
}

// gate is the work gate these deps describe — the choke point every writing
// pass takes its cards through.
//
// Derived on each call rather than built once and stored, for the same reason
// RunExit.CleanExit is: a stored copy is a second source that can disagree with
// the fields it came from, and a caller constructing ReconcileDeps directly
// (every test does) would hold a zero gate that silently admits nothing. It is
// a four-field value; constructing it costs nothing.
func (d ReconcileDeps) gate() WorkGate {
	return WorkGate{
		reachable:    d.Reachable,
		participates: d.Participates,
		daemonID:     d.DaemonID,
		identityFor:  d.IdentityFor,
	}
}

// aliveAgents reads the board agents running on this machine into a set. Every
// relaunching pass needs it and each used to build it from the same six lines
// until they were pulled into the one shared aliveAgentSet (board_stageagents.go)
// so this package's two independent readers of "who is alive" cannot drift.
// The bool reports whether the answer is usable at all: a nil lister or a failed
// lookup cannot establish liveness, and a pass that cannot establish liveness
// must do nothing rather than assume a card is dead.
func (d ReconcileDeps) aliveAgents(what string) (map[string]struct{}, bool) {
	return aliveAgentSet(d.LiveAgents, d.Logger, what)
}

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
func RunBoardReconcile(ctx context.Context, deps ReconcileDeps) {
	if deps.ListCards == nil || deps.ChainReview == nil {
		return
	}

	deps.Logger.Info().Msg("board reconcile started")

	// Recover a restart-orphaned handoff immediately, before the first wait. The
	// jitter applies only to subsequent cycles, so a restart-orphan is never made
	// to wait a full interval.
	reconcileOnce(ctx, deps)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(JitteredInterval(deps.Interval, BoardReconcileJitter)):
			reconcileOnce(ctx, deps)
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
func reconcileOnce(ctx context.Context, deps ReconcileDeps) {
	logger := deps.Logger
	gate := deps.gate()
	cards, err := deps.ListCards(ctx)
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
	if n := reconcileOrphanedHandoffs(gate.forReview(cards), deps, time.Now()); n > 0 {
		logger.Info().Int("launched", n).Msg("board reconcile: chained review for orphaned handoffs")
	}
	if n := reconcileShippedFailures(ctx, gate.forOwnWork(cards), deps, time.Now()); n > 0 {
		logger.Info().Int("cleared", n).Msg("board reconcile: cleared stale deploy-failed reds (shipped, or re-drove a shippable deploy)")
	}
	// The PR-loop re-drive runs BEFORE the stuck-running pass so a loop card
	// stranded by a restart is re-driven rather than reddened — the stuck pass
	// also skips it (doneStageLoopActive), but ordering makes the ownership clear.
	if n := reconcilePRLoops(ctx, gate.forTakeover(cards), deps); n > 0 {
		logger.Info().Int("redriven", n).Msg("board reconcile: re-drove stalled PR review→fix loops")
	}
	// An outage card is re-driven BEFORE the stuck-running pass: it is not a hang
	// to be reddened but a stage waiting on the substrate, relaunched uncharged
	// each tick (the backoff) until the substrate returns (SC-2307). It rides the
	// same forTakeover gate — an outage relaunch takes over the stage on this
	// machine exactly as a stuck-running reclaim does.
	if redriven, handedOver := reconcileOutage(ctx, gate.forTakeover(cards), deps, time.Now()); redriven > 0 || handedOver > 0 {
		logger.Info().Int("redriven", redriven).Int("handed_over", handedOver).
			Msg("board reconcile: re-drove stages waiting on the substrate, or told a person about one that is not waiting")
	}
	if n := reconcileStuckRunning(ctx, gate.forTakeover(cards), deps, time.Now()); n > 0 {
		logger.Info().Int("reddened", n).Msg("board reconcile: reddened stuck-running cards with no live agent")
	}
	// After the stuck pass: a failed stage the live exit path never reached (a
	// restart between the failure and its handling, a lost launch) is
	// relaunched here through the same charged retry policy, rather than left
	// red until a person clicks Retry (SC-5170).
	if n := reconcileFailedStages(ctx, gate.forTakeover(cards), deps, time.Now()); n > 0 {
		logger.Info().Int("relaunched", n).Msg("board reconcile: relaunched failed stages the live path did not reach")
	}
	// After the stuck pass, because a queued card is the one thing that pass is
	// built NOT to touch: BoardQueued exists so a just-decided card is not redded
	// (SC-1320), which left it watched by nothing at all (SC-3865). It takes the
	// same forTakeover gate as the other relaunching passes — starting the stage a
	// decision queued takes that stage over on this machine.
	if n := reconcileQueuedLaunch(ctx, gate.forTakeover(cards), deps, time.Now()); n > 0 {
		logger.Info().Int("launched", n).Msg("board reconcile: started stages left queued by an answered decision")
	}
	// Last: the passes above all act on cards that are still ON the board, while
	// this one reaches the runs whose card has left it — the orphan the close
	// gate cannot cover because the ticket was closed outside the board (1698).
	if n := reconcileOrphanedAgents(ctx, cards, deps); n > 0 {
		logger.Info().Int("stopped", n).Msg("board reconcile: stopped agents orphaned on closed tickets")
	}
}

// stageStalled reports whether a live agent has stopped making progress.
//
// An unknown agent (no probe, or a daemon that restarted and lost its progress
// map) is NOT treated as stalled: killing live work on absent evidence is the
// one failure this must never risk, and the container-liveness check has
// already established something is running.
func stageStalled(progress AgentProgressProbe, agentName string, now time.Time) (bool, SilenceReap) {
	if progress == nil {
		return false, SilenceReap{}
	}
	p, ok := progress(agentName)
	if !ok {
		return false, SilenceReap{}
	}
	stalled, idle := p.Stalled(now)
	return stalled, silenceReapOf(p, idle)
}

// doneStageLoopActive reports whether the card's newest done-stage marker is a
// PR-loop started marker — the review→fix loop is mid-flight rather than a plain
// deploy. Used to hand loop cards to the re-drive pass (:262) and to keep the
// generic stuck-running pass from redding them (:370); BOTH halves must match,
// because a loop card's half-agents come and go between rounds.
//
// The badge's finer question — which half is running — is doneStageLoopHalf,
// which this delegates to so the two answers are read off one marker
// inspection and the header set cannot drift between them (SC-3569, SC-4151 F15).
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

// doneStageStartedHalf names the loop half whose agent was last STARTED in the
// done stage, looking past a failure marker that landed on top of it.
//
// doneStageLoopHalf answers about the newest marker of any kind, which is what
// "is the loop mid-flight" needs. This answers the different question a red card
// raises: who would be running this stage, so that whether they still are can be
// asked at all.
//
// It reports nothing when the newest STARTED marker is a deploy or deploy-fix
// launch rather than a loop half. That is the deliberate narrowing
// AgentNamesForCard already documents for the deploy fixer: answering a
// deploy-path failure with a PR-loop container left over from an earlier round
// would turn an unreaped container into a false "still working".
func doneStageStartedHalf(comments []tracker.Comment) string {
	var newest tracker.Comment
	var half string
	found := false
	for _, c := range comments {
		t := strings.TrimSpace(c.Body)
		var h string
		switch {
		case strings.HasPrefix(t, PRReviewStartedHeader):
			h = DeployPhasePRReview
		case strings.HasPrefix(t, PRFixStartedHeader):
			h = DeployPhasePRFix
		case strings.HasPrefix(t, DeployStartedHeader), strings.HasPrefix(t, DeployFixStartedHeader):
			h = "" // a started marker, but not a loop half
		default:
			continue
		}
		if !found || commentNewer(c, newest) {
			newest, half, found = c, h, true
		}
	}
	return half
}

// deployEngineRunning reports a card inside the deploy engine's own bounded run:
// the newest done-stage marker is [human:deploy-started] and nothing has
// superseded it.
func deployEngineRunning(comments []tracker.Comment) bool {
	_, latest := latestStateInStage(comments, BoardDoneStage)
	return strings.HasPrefix(strings.TrimSpace(latest.Body), DeployStartedHeader)
}

// stuckGraceFor is how long THIS card is left alone before liveness is judged.
//
// A deploy reports no agent liveness at all — the engine runs in the daemon (or
// in a forwarded CLI call), not in a container the sweep can see — and its CI
// gate legitimately blocks for the whole of deployTimeout. Judging it by the
// ordinary grace would red a deploy that is merely waiting on CI and relaunch a
// second one on top of it, which is the regression that shipping the start
// marker would otherwise have caused (SC-3852). Bounded, not exempt: past the
// engine's own timeout plus the ordinary grace, a deploy that never returned is
// as dead as any other stage and is redded as usual.
//
// The marker set this reads is the FALLBACK path only — the machine that is not
// running the deploy, or one that restarted and lost the registry. Where the
// engine's own clock is knowable, stuckPastGrace uses it instead, which is what
// makes the grace mean the same thing on every deploy route rather than only on
// the one whose newest marker happens to be [human:deploy-started] (SC-4150).
func stuckGraceFor(derived BoardCard, comments []tracker.Comment) time.Duration {
	if derived.Stage == BoardDoneStage && deployEngineRunning(comments) {
		return deployTimeout + StuckRunningGrace
	}
	return StuckRunningGrace
}

// stuckPastGrace answers the only question the grace check needs: has this card
// sat long enough to be judged. Two clocks can answer it and they do not start
// together — the ticket's marker clock (StageEnteredAt) and the deploy engine's
// own, which does not start until DeployBranch is past the unbounded deployGate.
// Where this machine is running the deploy, the engine's clock is the truthful
// one; where it is not, the marker clock is all there is (SC-4150).
//
// The engine's clock covers every deploy entry route at once, because it is keyed
// by the in-flight run rather than by which marker is newest: the approve branch's
// [human:pr-review-passed] and the deploy fixer's [human:deploy-fix-started] left
// a deploy on the ordinary 15-minute grace with a 45-minute CI gate ahead of it.
func stuckPastGrace(derived BoardCard, card ReconcileCard, probe DeployRunProbe, now time.Time) bool {
	if derived.Stage == BoardDoneStage && probe != nil {
		if since, ok := probe(card.Key); ok {
			return now.Sub(since) >= deployTimeout+StuckRunningGrace
		}
	}
	return now.Sub(derived.StageEnteredAt) >= stuckGraceFor(derived, card.Comments)
}

// deployEngineActive reports whether THIS machine's deploy engine currently
// owns pmKey's card — DeployBranch is between deployRunQueued and
// deployRunFinished for it. Both the PR-loop's merge action and the deploy
// fixer's post-fix retry call DeployBranch synchronously with no started
// marker in between, so the done stage's newest STARTED marker still names the
// sub-agent that just finished, not this in-process window that owns none
// (SC-5396).
func deployEngineActive(probe DeployRunProbe, pmKey string) bool {
	if probe == nil {
		return false
	}
	_, ok := probe(pmKey)
	return ok
}

// recordedDeath reports whether the stage's agent is known to have died during
// this stage: it is absent from the live listing and this machine's agent
// record says it stopped AFTER the stage was entered, so the record is about
// the run the card shows and not about an earlier run of the same stage whose
// stop was already adjudicated. Absent evidence never counts: a nil or failing
// lister, an agent the record does not name, or a stop older than the stage
// all answer false and leave the card to the ordinary grace.
//
// names is every agent that can own the stage; a done-stage card has three,
// and a record for any of them that postdates the stage is this stage's death.
func recordedDeath(deps ReconcileDeps, names []string, alive map[string]struct{}, stageEnteredAt time.Time) (bool, time.Time) {
	if deps.StoppedAgents == nil || stageEnteredAt.IsZero() {
		return false, time.Time{}
	}
	for _, n := range names {
		if _, ok := alive[n]; ok {
			return false, time.Time{}
		}
	}
	stopped, err := deps.StoppedAgents()
	if err != nil {
		deps.Logger.Warn().Err(err).Msg("board reconcile: cannot list stopped agents, keeping the stuck grace")
		return false, time.Time{}
	}
	var newest time.Time
	for _, n := range names {
		at, ok := stopped[n]
		if !ok || at.IsZero() || !at.After(stageEnteredAt) {
			continue
		}
		if at.After(newest) {
			newest = at
		}
	}
	if newest.IsZero() {
		return false, time.Time{}
	}
	return true, newest
}

// reconcilePRLoops re-drives a loop card the live exit hook missed: a
// done/running card whose newest done marker is a loop-started marker and for
// which no loop half-agent is alive on this machine (a daemon restart lost the
// Stop event). driveLoop re-reads the recorded state and advances or escalates,
// idempotently (AdvancePRLoop's escalate no-ops on an already-open options
// block, and the alive-guard prevents racing a second launch). nil deps disable it.
// The alive-guard is the done stage's three-agent join (stageAgentNames), not
// the loop's two halves (SC-5591).
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
func reconcilePRLoops(ctx context.Context, drivable DrivableCards, deps ReconcileDeps) int {
	logger := deps.Logger
	if deps.DriveLoop == nil {
		return 0
	}
	alive, ok := deps.aliveAgents("PR loops")
	if !ok {
		return 0
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
		// A live done-stage agent owns the card — leave it; re-driving would race
		// a second launch onto the same step. Every agent the stage can run under
		// is asked, not just the two loop halves: the deploy fixer posts no loop
		// marker of its own, so a card the thread still shows mid-loop can be owned
		// by board-<key>-deployfix, and re-driving it re-ran the merge against the
		// branch that fixer was rebasing (SC-5591, the site SC-5396 missed).
		if _, ok := liveStageAgent(alive, card.Key, BoardDoneStage); ok {
			continue
		}
		if err := deps.DriveLoop(card.Key); err != nil {
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
// Not every card standing on an outage marker is waiting on a substrate. One
// whose host this daemon's proxy refused by policy is converted to a red
// naming the host and the config line and never re-driven (SC-5840); it is
// counted with the wait-bound handovers, which is what it is.
//
// A live agent for the stage means the relaunch already happened this cycle, so
// the card is left alone rather than racing a second launch onto the same stage
// — the same alive-guard reconcilePRLoops and reconcileStuckRunning use. nil
// deps or an unwired retry disable the pass (the package's "nil disables"
// convention); an unwired postFailed disables only the handover, leaving the
// indefinite wait rather than stranding the card with neither.
//
// Returns how many cards were re-driven and how many were handed to a human.
func reconcileOutage(ctx context.Context, drivable DrivableCards, deps ReconcileDeps, now time.Time) (redriven, handedOver int) {
	logger := deps.Logger
	if !deps.Retry.enabled() {
		return 0, 0
	}
	alive, ok := deps.aliveAgents("outage re-drive")
	if !ok {
		return 0, 0
	}
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if derived.State != BoardOutage {
			continue
		}
		// A live agent means the relaunch already happened this cycle — leave it.
		// Every agent that can own the stage is asked, not a name composed from
		// the stage: the done stage runs three of them (SC-5396).
		if _, ok := liveStageAgent(alive, card.Key, derived.Stage); ok {
			continue
		}
		if since, ok := outageRunSince(card.Comments, derived.Stage); ok && deps.PostFailed != nil && now.Sub(since) > OutageWaitBound {
			if handOverOutage(ctx, card.Key, derived, deps, now.Sub(since), since) {
				handedOver++
			}
			continue
		}
		// A host this daemon's own proxy refused is not a substrate to wait for:
		// it comes back only when a person edits proxy.domains, so the uncharged
		// re-drive would repeat a config gap at the reconcile interval for six
		// hours — which is exactly what it did (SC-5840). Converting the card to
		// a red here is what reaches a card parked before this daemon learned the
		// difference. Counted as a handover because that is what it is: the wait
		// is ended and a person is told, charging nothing.
		if blk, ok := deps.Retry.egressBlock(card.Key); ok {
			if postEgressBlockedFailure(ctx, card.Key, derived.Stage, card.Comments, blk, deps.PostFailed, logger) {
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
		if deps.Retry.relaunchOutage(card.Key, derived.Stage, logger) {
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
func hungLiveAgent(deps ReconcileDeps, agentName string, now time.Time, pmKey string, stage BoardStage) (proceed, silenced bool, reap SilenceReap) {
	logger := deps.Logger
	// A live container is not the same as a working agent: a hung agent looks
	// perfectly healthy here, which is why a hang was previously never
	// detected at all. Ask whether it is still making progress.
	stalled, silence := stageStalled(deps.Progress, agentName, now)
	if !stalled {
		return false, false, SilenceReap{} // genuinely working, however long it has been running
	}
	logger.Warn().Str("pm", pmKey).Str("stage", string(stage)).
		Dur("idle", silence.Idle).Dur("budget", silence.Budget).Str("outstanding", silence.Outstanding).
		Msg("board reconcile: agent alive but making no progress, treating as hung")
	// A hung agent still holds its container and workspace, so it must be
	// stopped before anything relaunches — otherwise two agents work the same
	// stage. A stop that fails (or is unwired) leaves the card alone rather
	// than risking that.
	if deps.StopAgent == nil {
		return false, false, SilenceReap{}
	}
	if err := deps.StopAgent(agentName); err != nil {
		logger.Warn().Err(err).Str("agent", agentName).
			Msg("board reconcile: cannot stop hung agent, leaving the card as-is")
		return false, false, SilenceReap{}
	}
	return true, true, silence
}

func reconcileStuckRunning(ctx context.Context, drivable DrivableCards, deps ReconcileDeps, now time.Time) int {
	if deps.PostFailed == nil {
		return 0
	}
	// Without a trustworthy liveness picture the pass must not red anything — a
	// probe blip is not evidence a card is dead.
	alive, ok := deps.aliveAgents("stuck-running")
	if !ok {
		return 0
	}

	reddened := 0
	for _, card := range drivable.cards {
		if reconcileOneStuckCard(ctx, card, alive, deps, now) {
			reddened++
		}
	}
	return reddened
}

// stuckCardLivenessVerdict decides whether a candidate card is dead enough to
// act on, and how it died: proceed reports that the pass may act, silenced that
// a LIVE agent was judged hung and stopped by this pass (uncharged), and reap
// carries what was observed. Extracted from reconcileOneStuckCard so the two
// clocks (recorded death, the stuck grace) and the hung-agent probe cost their
// own function rather than that one's complexity budget.
func stuckCardLivenessVerdict(card ReconcileCard, derived BoardCard, alive map[string]struct{}, deps ReconcileDeps, now time.Time) (proceed, silenced bool, reap SilenceReap) {
	logger := deps.Logger
	// While THIS machine's deploy engine is actively running this card
	// (deployRunQueued..deployRunFinished), the CI-gate/merge window runs no
	// board agent at all — the sub-agent that owned the PREVIOUS phase (the
	// reviewer that approved, the deploy fixer that resolved its conflict) has
	// already exited normally, and that ordinary exit is recorded as a stop
	// after StageEnteredAt exactly like a real death is. recordedDeath cannot
	// tell the two apart from the stop record alone, so it must not be
	// consulted here: this window's only clock is stuckPastGrace's
	// DeployRunProbe branch below, which already accounts for the CI gate's
	// own timeout (SC-5396).
	died, at := false, time.Time{}
	if !deployEngineActive(deps.DeployRun, card.Key) {
		died, at = recordedDeath(deps, stageAgentNames(card.Key, derived.Stage), alive, derived.StageEnteredAt)
	}
	if died {
		// The manager already recorded this stage's agent as stopped after the
		// stage began, and nothing handled the exit (the card is still running).
		// That is a death the machine has evidence for, so waiting out a grace
		// meant for silence would spend fifteen minutes on a fact it already
		// holds — measured on a killed implementation agent in the campaign
		// (SC-5327). It falls through to the vanished-agent path below unchanged.
		logger.Warn().Str("pm", card.Key).Str("stage", string(derived.Stage)).
			Time("stopped_at", at).Msg("board reconcile: agent recorded as stopped during this stage, skipping the stuck grace")
	} else if !stuckPastGrace(derived, card, deps.DeployRun, now) {
		// Young enough to still be genuine in-flight work.
		return false, false, SilenceReap{}
	}
	// silenced marks a stop THIS pass chose because a live agent stopped
	// making progress — a machine-chosen stop, not a stage failure, so it
	// must not consume the ticket's retry budget (SC-2447). A vanished
	// agent (no entry in alive) is a genuine, unexplained death and stays
	// on the charged path unchanged.
	if liveName, isLive := liveStageAgent(alive, card.Key, derived.Stage); isLive {
		// hungLiveAgent is handed the name that is actually alive, so the
		// progress probe asks about the right container (SC-5396).
		return hungLiveAgent(deps, liveName, now, card.Key, derived.Stage)
	}
	return true, false, SilenceReap{}
}

// completeStrandedPlanHandoff is the durable twin of the live watcher's
// completePlanningHandoff: a planning card whose plan is attached and whose
// handoff never landed is completed, not reddened. Reached only when the live
// watcher missed the exit — a daemon restart, a dropped hook — and only AFTER
// the liveness verdict, so a planner still alive and about to post its own
// handoff is never raced.
//
// Every clean-ending guard the live path honours needs its twin here; the
// asymmetry is what let this defect class recur twelve times in one night
// (SC-3149, and see stuckCardIsOursToRed). Posts through PostFailed, which is
// already how a non-failure body reaches the ticket from this pass
// (RunCancelledBody, board_reconcile_orphan.go).
//
// silenced and reap are the liveness verdict's own judgement (stuckCardLivenessVerdict):
// silenced true means THIS pass found the agent still alive, judged it hung
// and stopped it via hungLiveAgent — the run did not exit on its own. Posting
// the ordinary body in that case would misstate what happened and, because
// this completion returns before stuckRunningSilenceBody ever runs, would
// drop the stop from the record entirely — the exact trail SC-2447/SC-3074
// require. Word it for a reaped run and carry the observation along instead
// of silently losing it (SC-5090).
func completeStrandedPlanHandoff(ctx context.Context, card ReconcileCard, derived BoardCard, deps ReconcileDeps, silenced bool, reap SilenceReap) bool {
	if derived.Stage != BoardPlanning || !planAttachedAfterStart(card.Comments) {
		return false
	}
	body := planHandoffCompletedBody()
	logMsg := "board reconcile: planning card carries a plan newer than its start and no handoff; posted the handoff on the dead run's behalf"
	if silenced {
		body = planHandoffCompletedReapedBody(reap)
		logMsg = "board reconcile: planning card's agent was still live but silence-reaped by this pass; posted the handoff on the reaped run's behalf"
	}
	if err := deps.PostFailed(ctx, card.Key, body); err != nil {
		deps.Logger.Warn().Err(err).Str("pm", card.Key).
			Msg("board reconcile: cannot complete the planning handoff; falling through to the stuck-running red")
		return false
	}
	deps.Logger.Info().Str("pm", card.Key).Msg(logMsg)
	return true
}

// reconcileOneStuckCard applies the stuck-running judgement to a single card
// and reports whether it was reddened. Split out of reconcileStuckRunning so
// that function's per-card branching costs its own function rather than the
// loop's complexity budget.
func reconcileOneStuckCard(ctx context.Context, card ReconcileCard, alive map[string]struct{}, deps ReconcileDeps, now time.Time) bool {
	logger := deps.Logger
	derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
	if !stuckCardIsOursToRed(derived, card) {
		return false
	}
	failedType := failedTypeFor(derived.Stage)
	if failedType == "" {
		return false
	}
	proceed, silenced, reap := stuckCardLivenessVerdict(card, derived, alive, deps, now)
	if !proceed {
		return false
	}
	// A planning run that attached its plan before dying produced what the next
	// stage needs; redding it would re-plan an attached plan (SC-5090).
	if completeStrandedPlanHandoff(ctx, card, derived, deps, silenced, reap) {
		return false
	}
	// Repeated silence reaps are bounded and visible, identically to the live
	// failure watcher's cap (SC-3074): at or over MaxSilenceReaps this stops
	// relaunching and says a person is needed, naming the count once; skip
	// is set when the give-up marker is already on the thread, so a second
	// daemon reaching the same cap posts nothing more.
	failed, givingUp, skip := stuckRunningSilenceBody(failedType, derived.Stage, card.Comments, silenced, reap)
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
	body := markerBody(failed, silenceReapFieldOrder...)
	if err := deps.PostFailed(ctx, card.Key, body); err != nil {
		logger.Warn().Err(err).Str("pm", card.Key).
			Msg("board reconcile: cannot red stuck-running card")
		return false
	}
	// The relaunch below decides from the thread with this marker on it, as the
	// transition layer will see it (SC-5104).
	card.Comments = append(card.Comments, tracker.Comment{Body: body, Created: now})
	if silenced {
		if !givingUp {
			// A live agent this pass judged hung and stopped itself: uncharged,
			// like the live failure watcher's silence-reap path (SC-2447) — the
			// work did not fail, a judgement about the work did.
			deps.Retry.relaunchSilenceReap(card.Key, derived.Stage, logger)
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
	//
	// staleFailure inside tryRelaunch recomputes DeriveBoardCard(card.Comments,
	// ...) — the same derivation as `derived` above — so current == failed
	// always on this call and the stale-failure guard is a no-op here. It only
	// fires from handleBoardAgentExit, where the stage compared is the run's
	// own recorded exit.Stage rather than a fresh derivation from these same
	// comments.
	deps.Retry.tryRelaunch(ctx, card.Key, derived.Stage, card.Comments, nil, deps.DaemonID, logger)
	return true
}

// stuckRunningSilenceBody composes the reddening body for a stuck-running
// card and reports whether the SC-3074 cap means this pass gives up rather
// than relaunches (givingUp), and whether the card should be skipped
// entirely because the give-up marker was already posted for this stage
// (skip — the dedup that keeps two daemons from both posting it). Split out
// of reconcileStuckRunning so that function's branching stays inside the
// complexity gate.
func stuckRunningSilenceBody(failedType string, stage BoardStage, comments []tracker.Comment, silenced bool, reap SilenceReap) (m marker.Marker, givingUp, skip bool) {
	if !silenced {
		return failureMarker(failedType, stuckRunningReason(stage)), false, false
	}
	if silenceReapGaveUp(comments, stage) {
		return marker.Marker{}, false, true
	}
	stops := silenceReapCount(comments, stage) + 1
	if stops > MaxSilenceReaps {
		return silenceReapGiveUpMarker(failedType, stage, stops, comments, reap), true, false
	}
	return silenceReapMarker(failedType, reap), false, false
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

// shippableRedriveBackoff spaces the re-drive attempts on ONE pull request
// HEAD. The arm has no upper time bound on purpose — a card red overnight with
// a green PR is exactly what it exists to clear — so the pacing is what keeps
// a re-drive that starts nothing, or that starts and ends in another failure
// on the SAME head, to a handful of tries per head rather than one per tick.
// Keyed on the head rather than on the failure that prompted the check: a
// re-drive that starts always posts a fresh [human:deploy-failed] if it does
// not merge, and a key built from that marker's timestamp (the original
// SC-3640 shape) therefore never repeats and the backoff never engages — see
// redriveShippableDeploy for the head-keyed replacement and the launched path,
// which for the same reason must never clear this entry: "launched" answers
// only whether the re-drive's goroutine started (runDoneStage returns before
// openDraftPRAndReview's outcome is known), not whether it succeeded, so
// treating it as success and clearing the entry reproduces the same hole one
// call later. In memory like its sibling: the thread is the durable state,
// this is only pacing.
var shippableRedriveBackoff = newRecoveryBackoff(2*time.Minute, 30*time.Minute)

// reconcileShippedFailures clears the 695-class stale red two ways: a done-stage
// card whose newest marker is a deploy-failure but whose PR the forge reports
// MERGED (an out-of-band manual merge posted no marker) is retired with a
// [human:deployed] marker; one that is not merged but has become DEFINITELY
// SHIPPABLE — mergeable, with every check passing — is re-driven instead, so a
// pull request that turned green after the red no longer sits behind it until a
// person notices (SC-3640). DeriveBoardCard's existing supersession guard
// retires the marker's red on the next derivation either way. Reuses
// DeriveBoardCard verbatim so detection can never disagree with the board's
// rendered state. nil deps disable the corresponding arm. Returns the number of
// cards cleared or re-driven.
//
// It takes the forOwnWork gate rather than the raw board: posting [human:deployed]
// is a write on someone's ticket and must obey the ownership rule like every other
// write (SC-4063). It must NOT take forReview — that arm demands a reachable
// branch, and the branch of a merged PR is deleted at merge, so reachability would
// filter out exactly the cards this pass exists to clear. The shippable re-drive
// arm DOES push a branch, so it checks reachability itself (branchActionableHere)
// rather than moving the whole pass behind a gate the merged arm must not have.
func reconcileShippedFailures(ctx context.Context, drivable DrivableCards, deps ReconcileDeps, now time.Time) int {
	logger := deps.Logger
	if deps.MergedProbe == nil || deps.PostDeployed == nil {
		return 0
	}
	// Read once per pass, like every other relaunching pass. Unusable (nil
	// lister or a failed lookup) disables only the re-drive arm below: the
	// merged arm posts a marker about work that has already landed and needs
	// no liveness, while a re-drive over a live deploy is the SC-5396 class.
	alive, aliveKnown := deps.aliveAgents("shippable-deploy recovery")
	cleared := 0
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		// Only a done-stage failure that names a PR can be confirmed shipped: the
		// out-of-band merge posts no marker, so the forge's merged flag is the only
		// evidence the work landed.
		if derived.State != BoardFailed || derived.Stage != BoardDoneStage || derived.PRURL == "" {
			continue
		}
		merged, err := deps.MergedProbe(ctx, derived.PRURL)
		if err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).Str("pr", derived.PRURL).
				Msg("board reconcile: cannot probe PR merge status, leaving card as-is")
			continue
		}
		if merged {
			if err := deps.PostDeployed(ctx, card.Key, derived.PRURL); err != nil {
				logger.Warn().Err(err).Str("pm", card.Key).
					Msg("board reconcile: cannot post deployed marker for shipped PR")
				continue
			}
			cleared++
			continue
		}
		// Not merged is not "not shippable". The red was written from one
		// observation and never re-read, so a pull request that turned green a
		// minute later stayed red until a person noticed (SC-3640). The caution
		// the !merged rule carried is unchanged and carries over verbatim: the
		// arm below acts only on a DEFINITELY shippable state and never on an
		// unknown one.
		if redriveShippableDeploy(ctx, card, derived, deps, alive, aliveKnown, now) {
			cleared++
		}
	}
	return cleared
}

// redriveShippableDeploy re-drives the deploy for one red done-stage card whose
// pull request has since become definitely shippable. Reports whether a re-drive
// started.
//
// It charges no stage-retry attempt. Every other recovery in this file relaunches
// on a GUESS that another attempt might work and is bounded by DefaultStageRetries;
// this one acts on positive evidence that the deploy is ready to ship, and a spent
// budget must not keep a green, mergeable pull request behind a red card.
func redriveShippableDeploy(ctx context.Context, card ReconcileCard, derived BoardCard,
	deps ReconcileDeps, alive map[string]struct{}, aliveKnown bool, now time.Time) bool {
	logger := deps.Logger
	if deps.ShippableProbe == nil || deps.RetryDeploy == nil || !aliveKnown {
		return false
	}
	branch, ok := deployRedriveEligible(card, derived, deps, alive, now)
	if !ok {
		return false
	}
	shippable, headSHA, err := deps.ShippableProbe(ctx, derived.PRURL)
	if err != nil {
		logger.Warn().Err(err).Str("pm", card.Key).Str("pr", derived.PRURL).
			Msg("board reconcile: cannot probe whether the PR is shippable, leaving card as-is")
		return false
	}
	if !shippable {
		return false
	}
	// Keyed on the head, not on the failure marker: a re-drive that starts but
	// does not merge posts its own fresh [human:deploy-failed], and pacing on
	// THAT would reset every single cycle (SC-3640 round 2). A head that has
	// not moved since the last attempt cannot produce a different outcome; one
	// that has is genuinely new evidence and earns a fresh attempt even if the
	// previous head's backoff has not elapsed.
	key := card.Key + "/deploy-redrive/" + headSHA
	if !shippableRedriveBackoff.due(key, now) {
		return false
	}
	shippableRedriveBackoff.tried(key, now)
	launched, err := deps.RetryDeploy(card.Key)
	if err != nil {
		logger.Warn().Err(err).Str("pm", card.Key).Str("branch", branch).
			Msg("board reconcile: could not re-drive the deploy for a shippable PR")
		return false
	}
	if !launched {
		// A refusal started nothing — the card is where it was, and the backoff
		// keeps the next try off the tick.
		return false
	}
	// No clear() here on purpose: launched reports only that the re-drive's
	// goroutine started (runDoneStage returns before openDraftPRAndReview's
	// result is known), never that it shipped or even registered as live.
	// Clearing on launch discarded the wait this call just recorded on every
	// single re-drive, which is exactly how the backoff paced nothing (SC-3640
	// round 2) — measured concretely for the case that matters: the launched
	// work dies before ever posting a marker or a live agent, so
	// deployRedriveEligible's liveStageAgent/DeployRun checks above see nothing
	// and let the next tick straight through to this function again, leaving
	// the backoff as the only thing standing between that and a re-drive per
	// tick forever. Once the re-drive DOES register — a live agent, a running
	// DeployRun, or a moved head — deployRedriveEligible or the head check
	// above returns before this key is even consulted, so the stale entry left
	// here is inert rather than load-bearing.
	logger.Info().Str("pm", card.Key).Str("pr", derived.PRURL).Str("branch", branch).Str("head", headSHA).
		Msg("board reconcile: the failed deploy's PR is mergeable with every check green; re-drove the deploy")
	return true
}

// deployRedriveEligible is the pure half of the decision — what the thread and
// this machine say about whether a re-drive is allowed at all — and returns the
// branch the re-drive will run on. Kept separate from the forge probe so the
// rules are testable without one.
func deployRedriveEligible(card ReconcileCard, derived BoardCard, deps ReconcileDeps,
	alive map[string]struct{}, now time.Time) (branch string, ok bool) {
	// The newest done-stage marker must be the DEPLOY failure. A
	// [human:pr-review-failed] card derives to (done, failed) too, and it is a
	// verdict about the change's content — green CI does not answer it, and
	// re-driving would relaunch a reviewer that just declined, every tick.
	if !stageAlreadyFailed(card.Comments, BoardDoneStage) {
		return "", false
	}
	// Waiting on a person is the one state the machine may not resolve.
	if awaitingDecision(derived) || stagePausedOnOptions(card.Comments, BoardDoneStage) {
		return "", false
	}
	// A live done-stage agent (reviewer, PR fixer, deploy fixer) owns this card;
	// a deploy running in THIS process registers no agent, so the engine's own
	// registry is the second half of the same question (SC-5396, SC-4150).
	if _, live := liveStageAgent(alive, card.Key, BoardDoneStage); live {
		return "", false
	}
	if deps.DeployRun != nil {
		if _, running := deps.DeployRun(card.Key); running {
			return "", false
		}
	}
	// The live path acts first, as on every other durable recovery. There is no
	// upper bound on purpose: a card nobody looked at overnight is the case.
	state, failed := latestStateInStage(card.Comments, BoardDoneStage)
	if state != BoardFailed || now.Sub(failed.Created) < FailedRecoveryGrace {
		return "", false
	}
	// The re-drive PUSHES this branch (openDraftPR), so a machine that cannot
	// resolve it would turn a stale red into a fresh push failure (SC-652).
	branch = doneStageBranch(card.Comments, derived)
	if branch == "" || !branchActionableHere(BoardCard{Branch: branch}, deps.Reachable) {
		return "", false
	}
	return branch, true
}

// reconcileOrphanedHandoffs launches the missed review for every card whose
// newest [human:ready-for-review] handoff is still waiting for the review that
// judges it. It reuses DeriveBoardCard verbatim so detection can never disagree
// with the board's rendered state.
//
// The orphan condition is said twice on purpose. handoffAwaitsReview is what the
// pass MEANS — a handoff newer than the newest verification marker — and the
// placement check is the board agreeing. It used to be the placement alone, on
// the claim that "any verification marker would make the furthest stage
// verification, so implementation/done structurally means no verification marker
// exists at all". That claim was false for a SECOND-round handoff: a card
// reworked after a failing verdict carried a verification marker forever, so the
// pass built for exactly this card could never see it, and the card looped on
// Rework for 21 hours (SC-4958, on SC-430's recovery). A pass whose correctness
// rests on a rank accident in another file is one edit away from blind again.
//
// It still subsumes ApplyFix's verification-running guard. It does NOT rely on
// ApplyTransition to deduplicate: that guard (isDuplicateDrop) fires only once
// a verification marker reads "running", and the launch this pass has to avoid
// happens in the window BEFORE such a marker exists — the seconds between a
// board fix run's handoff and its own [human:review-started]. The old comment
// here claimed "the two can never double-launch a review"; it was true of the
// two daemon paths racing each other and false of a daemon racing a container,
// which is the race that shipped two reviewers for one handoff on SC-5396
// (SC-5476). What prevents it now is the handoff saying so: a `review: inline`
// handoff whose implementation container is still alive is a review in flight,
// not an orphan, and this pass leaves it — while an inline handoff whose
// container is gone is chained exactly as before, so SC-430's recovery is
// untouched.
//
// The reachability gate guarding the chain now lives upstream: this pass receives
// DrivableCards from the forReview gate, so a review is chained only for a handoff
// whose branch this machine can resolve (local ref or on origin). A board-context
// fix leaves its branch local on the machine that produced it, so a daemon on
// another machine is never handed that card and leaves it for one that can reach
// the branch — never starting a review it could never satisfy (SC-652, now
// enforced by construction rather than a per-path check, SC-2047). Returns the
// number of reviews launched.
func reconcileOrphanedHandoffs(drivable DrivableCards, deps ReconcileDeps, now time.Time) int {
	logger := deps.Logger
	launched := 0
	alive, aliveKnown := deps.aliveAgents("orphaned-handoffs")
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		if !handoffAwaitsReview(card.Comments) || derived.Stage != BoardImplementation || derived.State != BoardDone {
			continue
		}
		if inlineReviewerOwnsHandoff(card.Comments, card.Key, alive, aliveKnown, now) {
			logger.Debug().Str("pm", card.Key).
				Msg("board reconcile: handoff says its own container is reviewing it and that container is alive, leaving it")
			continue
		}
		// Skip-and-leave when the commits are not DEFINITELY present — a clean
		// absence (a retry that never pushed) or an unreadable check (this machine
		// cannot reach the repo). A periodic scan must never red a card another
		// machine can serve; the loud failure lives on the live chain, and only on
		// a definite absence (SC-2403).
		if commitPresenceForHandoff(card.Comments, derived.Branch, deps.CommitsPresent).Status != ProbePresent {
			logger.Warn().Str("pm", card.Key).Str("branch", derived.Branch).
				Msg("board reconcile: handoff commits not verifiable on this machine, leaving it")
			continue
		}
		if err := deps.ChainReview(card.Key); err != nil {
			logger.Warn().Err(err).Str("pm", card.Key).Msg("board reconcile: cannot chain review for orphaned handoff")
			continue
		}
		launched++
	}
	return launched
}
