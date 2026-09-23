package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gethuman-sh/human/internal/tracker"
)

// FailedRecoveryGrace is how long a failed stage is left to the live exit path
// before the durable pass considers it. The live path relaunches a retryable
// failure the moment it processes the exit; this pass exists for the moment
// that never came — a restart between the failure and its handling, a comment
// read that failed, a launch that errored before anything started — and must
// not race the live path on the failures it does handle.
const FailedRecoveryGrace = 5 * time.Minute

// FailedRecoveryBound is how long after its failure a stage may still be
// relaunched by this pass. A failure nobody could recover in six hours is a
// person's, not a timer's: past it the card stays red with the attempts on
// record, and the pass stops re-reading a thread it cannot move. Mirrors
// OutageWaitBound, which draws the same line for a substrate that never comes
// back.
const FailedRecoveryBound = 6 * time.Hour

// failedRecoveryGiveUpSentinel is the fixed substring every failed-recovery
// give-up marker body carries — how a later pass (or a second daemon reaching
// the bound at the same time) recognises this pass already said it is done
// trying, and does not repeat itself. Deliberately NOT "stopped relaunching
// after waiting": silenceReapGiveUpSentinel ("stopped relaunching after",
// board_failure.go) is matched with strings.Contains against any *-failed
// marker regardless of which pass posted it, so a superstring of it here made
// every give-up this pass posts also read as a silence-reap give-up —
// disabling the silence-reap relaunch cap and the stuck-running sweep for
// that stage forever (caught in review; see the two predicates' own tests).
// The wording must stay disjoint from silenceReapGiveUpSentinel in both
// directions, not merely different.
const failedRecoveryGiveUpSentinel = "gave up relaunching after waiting"

// failedRecoveryGiveUpReason composes the line posted once a failed stage has
// sat past FailedRecoveryBound: the durable pass stops probing it and says a
// person is needed, naming how long it waited so the record does not read as
// plain silence. Mirrors outageHandoverBody, the same line drawn for a
// substrate that never comes back — including carrying the ORIGINAL failure
// reason (derived.Error, the board badge/tooltip text) into the first line,
// so the card does not lose its diagnosis the moment the pass gives up on it.
func failedRecoveryGiveUpReason(stage BoardStage, reason string, waited time.Duration) string {
	what := strings.TrimSpace(reason)
	if what == "" {
		what = "no reason was recorded"
	}
	return fmt.Sprintf("the daemon %s %s on the %s stage and is not relaunching again — this needs a "+
		"person: %s. The attempts already spent stay on record; the wait itself charged nothing further.",
		failedRecoveryGiveUpSentinel, waited.Round(time.Minute).String(), stage, what)
}

// failedRecoveryGaveUp reports whether the give-up marker has already been
// posted for stage, so a second daemon — or the same one on a later tick —
// reaching the bound for the same stage posts nothing more.
func failedRecoveryGaveUp(comments []tracker.Comment, stage BoardStage) bool {
	for _, c := range comments {
		s, st, ok := ClassifyMarker(c.Body)
		if !ok || s != stage || st != BoardFailed {
			continue
		}
		if strings.Contains(c.Body, failedRecoveryGiveUpSentinel) {
			return true
		}
	}
	return false
}

// failedRecoveryBackoff spaces this pass's attempts on one stage. A relaunch
// that started nothing charges no budget (SC-5104), so without a clock of its
// own the pass would retry a persistent launch error at the reconcile interval
// for the whole of FailedRecoveryBound; doubling from the interval to half an
// hour keeps that to a handful of tries and a handful of log lines. In memory
// on purpose: the thread and the retry counter are the durable state, this is
// only pacing, and a restart resetting it costs one extra try.
var failedRecoveryBackoff = newRecoveryBackoff(2*time.Minute, 30*time.Minute)

type recoveryBackoff struct {
	mu   sync.Mutex
	min  time.Duration
	max  time.Duration
	next map[string]time.Time
	wait map[string]time.Duration
}

func newRecoveryBackoff(minWait, maxWait time.Duration) *recoveryBackoff {
	return &recoveryBackoff{min: minWait, max: maxWait, next: map[string]time.Time{}, wait: map[string]time.Duration{}}
}

func (b *recoveryBackoff) due(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	next, ok := b.next[key]
	return !ok || !now.Before(next)
}

// tried schedules the next attempt, doubling the wait each time.
func (b *recoveryBackoff) tried(key string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	wait := b.wait[key]
	if wait == 0 {
		wait = b.min
	} else {
		wait = min(wait*2, b.max)
	}
	b.wait[key] = wait
	b.next[key] = now.Add(wait)
}

func (b *recoveryBackoff) clear(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.next, key)
	delete(b.wait, key)
}

func (b *recoveryBackoff) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next = map[string]time.Time{}
	b.wait = map[string]time.Duration{}
}

// reconcileFailedStages is the durable half of F3: every failed stage with
// budget left, no live agent and no reason to wait is relaunched through the
// same StageRetry policy the live exit path uses — charged only for launches
// that started, a needs-input relaunched only without an open question, a
// stale failure retired. Before it, a failed card whose exit event was lost
// was reached by no pass at all: reconcileStuckRunning needs a running card,
// reconcileOutage an outage, reconcileQueuedLaunch an answered decision, and
// everything else was a person's click (SC-4244, SC-5170).
//
// What it leaves alone, and why: a done-stage failure (the deploy has its own
// recovery — the shipped-failures probe and the deploy fixer — and a relaunch
// here would re-run a merge); a stage whose agent is alive on this machine
// (the relaunch already happened); a card paused on a decision (waiting on a
// person is the one state the machine may not resolve); a silence-reap give-up
// (the machine already said it will not relaunch); a standing plan-stuck
// escalation (refuseIfUnplanned already said it once, to a person — relaunching
// planning would repost it, SC-2990); a failure younger than FailedRecoveryGrace
// (the live path's). The retry policy itself supplies the rest: the attempt
// cap, the deliberate exits, the outage that goes uncharged.
//
// A failure older than FailedRecoveryBound is different from the rest of that
// list: the pass does not merely skip it, it gives up on it once and records
// that it did (recordFailedRecoveryGiveUp), mirroring handOverOutage — a card
// the pass tried and stopped on must read differently from one no pass ever
// reached.
//
// Known limitation: a rework build's crash is NOT reached here. A crashed
// rework implementation derives at whatever stage is furthest along (usually
// verification or done — "furthest stage wins"), so derived.State is not
// BoardFailed and recoverableFailure never sees it, even though the live exit
// path's own rework relaunch (isReworkTransition, board_retry.go) does cover
// it. Closing that gap needs a durable check keyed on the *-failed marker
// itself rather than on the derived card state.
//
// Returns how many stages it relaunched.
func reconcileFailedStages(ctx context.Context, drivable DrivableCards, deps ReconcileDeps, now time.Time) int {
	logger := deps.Logger
	if !deps.Retry.enabled() {
		return 0
	}
	alive, ok := deps.aliveAgents("failed-stage recovery")
	if !ok {
		return 0
	}
	relaunched := 0
	for _, card := range drivable.cards {
		derived := DeriveBoardCard(card.Comments, tracker.CategoryUnstarted, false)
		// Checked before the bound/eligibility switch: a live agent means a
		// relaunch already happened this cycle (or a moment ago, racing the
		// thread read), even when the thread itself still shows the old
		// failure because the fresh *-started marker has not landed yet — a
		// give-up must never be posted over a run that is actually in flight.
		if _, ok := alive[agentNameFor(card.Key, derived.Stage)]; ok {
			continue
		}
		switch recoverableFailure(card.Key, card.Comments, derived, deps.Retry, now) {
		case recoveryLeaveAlone:
			continue
		case recoveryBoundExceeded:
			// The pass has been trying since the failure was fresh; past
			// FailedRecoveryBound it stops and says so, once, exactly like
			// handOverOutage does for a substrate that never came back — a
			// person reading the card must be able to tell the durable pass
			// gave up from one that never reached it at all.
			recordFailedRecoveryGiveUp(ctx, card.Key, card.Comments, derived, deps, now)
			continue
		}
		key := card.Key + "/" + string(derived.Stage)
		if !failedRecoveryBackoff.due(key, now) {
			continue
		}
		failedRecoveryBackoff.tried(key, now)
		// No retry note (nil commenter): the standing *-failed marker is the
		// trail record, as on the stuck-running path. The policy reads the
		// thread this pass read, and relists before it launches.
		if deps.Retry.tryRelaunch(ctx, card.Key, derived.Stage, card.Comments, nil, deps.DaemonID, logger) {
			failedRecoveryBackoff.clear(key)
			logger.Info().Str("pm", card.Key).Str("stage", string(derived.Stage)).
				Msg("board reconcile: relaunched a failed stage the live path did not")
			relaunched++
		}
	}
	return relaunched
}

// recordFailedRecoveryGiveUp posts the once-only marker that says the durable
// pass stopped trying on this stage, so a card past FailedRecoveryBound reads
// as "the pass tried and gave up" rather than as silence indistinguishable
// from a card no pass ever reached. Deduplicated by failedRecoveryGaveUp so a
// second daemon — or the same one on a later tick — says nothing further.
//
// derived is the caller's already-computed card, read for two things: Stage,
// and Error — the board badge/tooltip text (failureReason of the newest
// failed marker, board_state.go) — carried into the give-up body exactly as
// outageHandoverBody carries it, so the card does not lose its original
// diagnosis the moment the pass gives up on it.
func recordFailedRecoveryGiveUp(ctx context.Context, pmKey string, comments []tracker.Comment, derived BoardCard, deps ReconcileDeps, now time.Time) {
	logger := deps.Logger
	stage := derived.Stage
	if deps.PostFailed == nil || failedRecoveryGaveUp(comments, stage) {
		return
	}
	failedType := failedTypeFor(stage)
	if failedType == "" {
		return
	}
	_, failed := latestStateInStage(comments, stage)
	waited := now.Sub(failed.Created)
	body := markerBody(failureMarker(failedType, failedRecoveryGiveUpReason(stage, derived.Error, waited)))
	if err := deps.PostFailed(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board reconcile: cannot record failed-recovery give-up, leaving the card as-is")
		return
	}
	logger.Warn().Str("pm", pmKey).Str("stage", string(stage)).Dur("waited", waited).
		Msg("board reconcile: failed-recovery bound exceeded, giving up and recording it")
}

// recoveryVerdict is recoverableFailure's judgement of a failed stage's card.
type recoveryVerdict int

const (
	// recoveryLeaveAlone: not this pass's to touch — a decision is open, a
	// give-up (silence-reap's or this pass's own) already stands, the state or
	// stage is not one it drives, or the failure is younger than
	// FailedRecoveryGrace and still the live path's to handle.
	recoveryLeaveAlone recoveryVerdict = iota
	// recoveryEligible: within the grace..bound window — the caller may relaunch it.
	recoveryEligible
	// recoveryBoundExceeded: failed, and older than FailedRecoveryBound — the
	// pass gives up and records that it did, exactly once.
	recoveryBoundExceeded
)

// recoverableFailure is the pure half of the pass's decision: whether this
// card's failure is one the machine may still act on, judged from the thread
// and the retry policy's own verdict on the recorded exit. Liveness and
// pacing are the caller's. retry is read once, only for the bound branch (see
// below) — it never changes anything within the grace..bound window, where
// the caller's own tryRelaunch already applies the identical policy and a
// non-retryable exit there is simply left red, charging and posting nothing.
func recoverableFailure(pmKey string, comments []tracker.Comment, derived BoardCard, retry StageRetry, now time.Time) recoveryVerdict {
	if derived.State != BoardFailed {
		return recoveryLeaveAlone
	}
	stage := derived.Stage
	if stageRank[stage] < stageRank[BoardPlanning] || stage == BoardDoneStage {
		return recoveryLeaveAlone
	}
	if awaitingDecision(derived) || stagePausedOnOptions(comments, stage) {
		return recoveryLeaveAlone
	}
	if silenceReapGaveUp(comments, stage) {
		return recoveryLeaveAlone
	}
	if failedRecoveryGaveUp(comments, stage) {
		return recoveryLeaveAlone
	}
	state, failed := latestStateInStage(comments, stage)
	if state != BoardFailed {
		return recoveryLeaveAlone
	}
	// A standing plan-stuck escalation classifies to (BoardPlanning, BoardFailed)
	// exactly like an ordinary refusal, and both are seen here as a planning-stage
	// failure. But the escalation was already said once, to a person, by
	// refuseIfUnplanned (SC-2990) — relaunching planning posts a fresh
	// [human:planning-started], which becomes the newest planning-stage marker
	// and defeats that guard's own dedup (newestIsRefusal && isPlanStuck), so
	// the very next refused implementation launch posts a second escalation.
	// Reads the same marker refuseIfUnplanned's dedup guard does, so the two
	// never disagree about what "the standing escalation" is.
	if stage == BoardPlanning && isPlanStuck(failed.Body) {
		return recoveryLeaveAlone
	}
	age := now.Sub(failed.Created)
	if age < FailedRecoveryGrace {
		return recoveryLeaveAlone
	}
	if age > FailedRecoveryBound {
		// A give-up says "the pass tried and stopped" — that must not be
		// posted on a card this pass was never going to touch. decisionOpen is
		// always false here (an open decision already left via
		// awaitingDecision/stagePausedOnOptions above), so classifyRelaunch
		// reads only the recorded exit: relaunchNone means a deliberate stop
		// (needs-human-work) or an exit this policy does not recognise — the
		// ticket's own "left alone" list — and the card is left exactly as a
		// pass that never reached it would leave it, with no marker at all.
		outcome, recorded := retry.Outcome(pmKey, stage)
		if classifyRelaunch(outcome, recorded, false) == relaunchNone {
			return recoveryLeaveAlone
		}
		return recoveryBoundExceeded
	}
	return recoveryEligible
}
