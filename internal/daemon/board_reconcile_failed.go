package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

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

// recoveryBoundWarned holds the stages whose bound the pass has already
// logged, so the moment a card goes from "probed every backoff tick" to
// "never touched again" leaves exactly one line in this host's log rather
// than none, or one per tick forever. A log line, not a marker: a marker at
// the bound collided with the sentinel scanners twice (SC-5170).
var recoveryBoundWarned sync.Map

// warnRecoveryBoundOnce logs, once per stage, that a failure has aged past
// FailedRecoveryBound and the pass has stopped considering it.
func warnRecoveryBoundOnce(comments []tracker.Comment, derived BoardCard, now time.Time, pmKey string, logger zerolog.Logger) {
	if derived.State != BoardFailed {
		return
	}
	state, failed := latestStateInStage(comments, derived.Stage)
	if state != BoardFailed || now.Sub(failed.Created) <= FailedRecoveryBound {
		return
	}
	key := pmKey + "/" + string(derived.Stage) + "/" + failed.Created.UTC().Format(time.RFC3339)
	if _, seen := recoveryBoundWarned.LoadOrStore(key, struct{}{}); seen {
		return
	}
	logger.Warn().Str("pm", pmKey).Str("stage", string(derived.Stage)).Dur("age", now.Sub(failed.Created)).
		Msg("board reconcile: failed stage is past the recovery bound; leaving it to a person")
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
// A failure older than FailedRecoveryBound is left alone too: the standing
// *-failed marker and the attempts on record are the trail, and `human fsm
// where` says the bound has passed. The pass posts nothing at the bound — a
// second *-failed marker there carried the original reason into a body every
// sentinel scanner reads, and twice collided with the silence-reap sentinels
// (SC-5170, rounds 2 and 3); a person's card is a person's card.
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
		if !recoverableFailure(card.Comments, derived, now) {
			warnRecoveryBoundOnce(card.Comments, derived, now, card.Key, logger)
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

// recoverableFailure is the pure half of the pass's decision: whether this
// card's failure is one the machine may still act on, judged from the thread
// alone. Liveness and pacing are the caller's; the retry policy's own rules
// (the attempt cap, the deliberate exits, the outage that goes uncharged)
// apply inside tryRelaunch, unchanged.
func recoverableFailure(comments []tracker.Comment, derived BoardCard, now time.Time) bool {
	if derived.State != BoardFailed {
		return false
	}
	stage := derived.Stage
	if stageRank[stage] < stageRank[BoardPlanning] || stage == BoardDoneStage {
		return false
	}
	if awaitingDecision(derived) || stagePausedOnOptions(comments, stage) {
		return false
	}
	if silenceReapGaveUp(comments, stage) {
		return false
	}
	state, failed := latestStateInStage(comments, stage)
	if state != BoardFailed {
		return false
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
		return false
	}
	age := now.Sub(failed.Created)
	return age >= FailedRecoveryGrace && age <= FailedRecoveryBound
}
