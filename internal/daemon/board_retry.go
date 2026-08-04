package daemon

import (
	"context"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// StageExit is the closed vocabulary an agent records in its stage report
// before returning. It is a named type, not a bare string, so a switch that
// decides on it can be marked `//exhaustive:enforce` and fail the build when a
// member is added without being handled. That is not hypothetical: ExitDone
// joined this vocabulary and classifyRelaunch's `default` silently swallowed
// it, which stranded planning runs that had produced a good plan (SC-3376).
type StageExit string

// Exit classes an agent records in its stage report before returning. They are
// the vocabulary of the prompts' exit contract; the board only has to tell
// "another attempt could plausibly fix this" from "it could not".
const (
	ExitRetryable      StageExit = "retryable"
	ExitNeedsInput     StageExit = "needs-input"
	ExitNeedsHumanWork StageExit = "needs-human-work"
	ExitDone           StageExit = "done"
	// ExitOutage records that the substrate a stage needs was unreachable — a
	// credential store timeout, a tracker it could not reach. The work was not
	// attempted, so it is relaunched with backoff and NEVER charged against
	// DefaultStageRetries (SC-2307). Distinct from ExitRetryable, which is a
	// flake or a dead container that a bounded, immediate relaunch absorbs.
	ExitOutage StageExit = "outage"
)

// ReapSilenceErrorType is the sentinel prefix the zombie sweep's synthesized
// StopFailure event carries in its ErrorType when the reap was a silence-reap
// — the agent went silent (no hook event AND no transcript output) past its
// idle budget, rather than genuinely dying. The full sentinel is
// "reaped-silent:<idle>" (e.g. "reaped-silent:18m0s"); silenceReapIdle parses
// the idle back out. A machine-chosen stop like this must never consume the
// stage's automatic-retry budget — the work did not fail, a judgement about
// the work did (SC-2447).
const ReapSilenceErrorType = "reaped-silent"

// silenceReapIdle reports whether errorType is a silence-reap sentinel and,
// if so, the idle duration it carries formatted for a human to read (as
// produced by ReapReason.Idle.Round(time.Second).String() at the point the
// sentinel was composed). ok is false for any other errorType, including the
// empty string a genuine StopFailure carries.
func silenceReapIdle(errorType string) (string, bool) {
	rest, ok := strings.CutPrefix(errorType, ReapSilenceErrorType+":")
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// DefaultStageRetries bounds automatic relaunches of one stage. Two is chosen
// against the failure it exists for: a flaky check or a container that died
// almost always passes on the next attempt, while a stage that is genuinely
// broken fails the same way every time and should reach a human quickly rather
// than burn tokens proving it.
const DefaultStageRetries = 2

// StageRetry relaunches a stage that failed for a reason another attempt could
// plausibly fix, so a run does not stop at the first crash and wait for someone
// to click Retry.
//
// It deliberately does not invent a launch path. The failure is recorded first,
// exactly as before, and the relaunch is then the same in-place retry
// transition a human's Retry gesture issues — so every existing guard applies
// unchanged: the idempotency check, the cross-daemon claim arbiter, and the
// stage-specific prompt each retry path already builds.
type StageRetry struct {
	// Outcome reports the exit class the stage recorded, and whether it
	// recorded one at all.
	Outcome func(pmKey string, stage BoardStage) (StageExit, bool)
	// Attempts increments and returns how many times this stage has been
	// relaunched automatically.
	Attempts func(pmKey string, stage BoardStage) (int, error)
	// Reset clears the attempt count. Called when the stage finishes cleanly,
	// so a later, unrelated failure gets a full budget instead of inheriting a
	// spent one.
	Reset func(pmKey string, stage BoardStage)
	// Relaunch issues the in-place retry transition and reports whether a launch
	// ACTUALLY happened — false when the transition was refused (e.g. the plan gate
	// started nothing), which must never be charged as an attempt (SC-2989).
	Relaunch func(pmKey string, stage BoardStage) (launched bool, err error)
	// Uncount rolls back one charged attempt when a bounded relaunch turned out to
	// be a refusal (nothing launched). Optional/nil-safe: an unset Uncount leaves
	// the counter as-is, matching the previous behaviour.
	Uncount func(pmKey string, stage BoardStage)
	// Max bounds automatic relaunches; zero means DefaultStageRetries.
	Max int
}

// enabled reports whether enough collaborators are wired to retry anything.
// An unconfigured StageRetry leaves the previous behaviour untouched.
func (r StageRetry) enabled() bool {
	return r.Outcome != nil && r.Attempts != nil && r.Relaunch != nil
}

func (r StageRetry) max() int {
	if r.Max <= 0 {
		return DefaultStageRetries
	}
	return r.Max
}

// relaunchKind decides how a failed stage is relaunched, if at all. It splits
// the old boolean mayRelaunch into three because an outage must be relaunched on
// a DIFFERENT path from a flake: uncharged and backoff-driven rather than
// bounded and immediate.
type relaunchKind int

const (
	relaunchNone    relaunchKind = iota // deliberate/unparseable exit: leave for a human
	relaunchBounded                     // flake / dead container / undiagnosed death: charged, capped
	relaunchOutage                      // substrate down: uncharged, reconcile-backoff relaunch
)

// classifyRelaunch decides, from the recorded exit alone, how (if at all) a
// failed stage is relaunched.
//
// An UNRECORDED outcome stays bounded: the agent died before it could write one,
// which is exactly the crash an automatic retry exists to absorb, and the
// attempt cap keeps a vanished agent from looping unbounded — an undiagnosed
// crash must never be mistaken for an outage's indefinite backoff. A recorded
// ExitOutage is the substrate being down, relaunched uncharged. ExitRetryable is
// a flake, relaunched charged. Anything else is a deliberate exit we do not
// recognise and is left for a human rather than looped on a sentence we cannot
// parse.
//
// ExitDone is the contradiction case, and it is relaunched rather than left red.
// This classifier only runs once a stage has already been judged failed for
// finishing without its done-marker, so a recorded "done" means the agent
// believes it succeeded while the board holds no evidence that it did. Markers
// are what advance a card, so as far as the pipeline is concerned the stage did
// not complete, and a bounded relaunch is the remedy every other incomplete
// stage gets. Sitting in relaunchNone it was treated identically to
// needs-human-work — "deliberate, ask a human" — which stranded planning runs
// that had produced a perfectly good plan and only missed the marker. The
// attempt cap bounds it, and a stage that really had finished re-posts a
// latest-wins marker rather than duplicating work.
func classifyRelaunch(outcome StageExit, recorded bool) relaunchKind {
	if !recorded {
		return relaunchBounded
	}
	// Every member is listed on purpose, and the linter enforces it: adding a
	// StageExit without deciding here is the exact failure this switch already
	// shipped once. The `default` stays as a runtime guard for a value read off
	// the wire that the type system never saw.
	//exhaustive:enforce
	switch outcome {
	case ExitOutage:
		return relaunchOutage
	case ExitRetryable, ExitDone:
		return relaunchBounded
	case ExitNeedsInput, ExitNeedsHumanWork:
		return relaunchNone
	default:
		return relaunchNone
	}
}

// reset clears a stage's attempt count after a clean finish.
func (r StageRetry) reset(pmKey string, stage BoardStage) {
	if r.Reset != nil {
		r.Reset(pmKey, stage)
	}
}

// uncount rolls back a charged attempt on a refused relaunch. Nil-safe so an
// unconfigured policy is unchanged.
func (r StageRetry) uncount(pmKey string, stage BoardStage) {
	if r.Uncount != nil {
		r.Uncount(pmKey, stage)
	}
}

// tryRelaunch decides and, when warranted, relaunches the stage. It reports
// whether the caller may consider the failure handled — false means the normal
// failed-marker path must run and the card should red as before.
//
// The failed marker is posted by the caller BEFORE this runs: the retry
// transitions all require a card that derives to a failed state, and the
// ticket's trail should record what actually happened rather than hiding a
// crash behind a silent re-run.
func (r StageRetry) tryRelaunch(ctx context.Context, pmKey string, stage BoardStage, commenter tracker.Commenter, daemonID string, logger zerolog.Logger) bool {
	if !r.enabled() {
		return false
	}
	outcome, recorded := r.Outcome(pmKey, stage)
	switch classifyRelaunch(outcome, recorded) {
	case relaunchNone:
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Str("exit", string(outcome)).
			Msg("board retry: stage exit is not retryable, leaving the card for a human")
		return false
	case relaunchOutage:
		return r.relaunchOutage(pmKey, stage, logger)
	}

	// relaunchBounded: the charged, capped path for a flake, a dead container, or
	// an undiagnosed death.
	attempt, err := r.Attempts(pmKey, stage)
	if err != nil {
		// Without a trustworthy count an automatic relaunch could loop, so fall
		// back to the human path rather than risk it.
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: cannot read the attempt count, leaving the card for a human")
		return false
	}
	if attempt > r.max() {
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Int("attempt", attempt).
			Msg("board retry: attempts exhausted, leaving the card for a human")
		return false
	}

	launched, err := r.Relaunch(pmKey, stage)
	if err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: relaunch failed, leaving the card as failed")
		return false
	}
	if !launched {
		// The relaunch was refused — the plan gate (or another launch guard) started
		// nothing. A refusal is not a launch: roll the charged attempt back so it
		// spends none of the budget a genuine crash is entitled to, post no
		// "Automatic retry" note, and report unhandled. The refusal has already
		// routed the card itself (SC-2989).
		r.uncount(pmKey, stage)
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Int("attempt", attempt).
			Msg("board retry: relaunch refused (nothing started); attempt not charged")
		return false
	}
	r.note(ctx, pmKey, stage, attempt, outcome, recorded, commenter, daemonID, logger)
	logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Int("attempt", attempt).
		Msg("board retry: stage relaunched automatically")
	return true
}

// relaunchOutage re-drives a stage that reported the substrate was down. The
// retry budget is deliberately untouched: an outage costs time and nothing
// else, so it retries on the reconcile interval as its backoff until the
// substrate returns or the wait outlives OutageWaitBound (SC-2307/SC-2851).
// Reads and bumps no counter — that is the whole point, an outage is never
// allowed to exhaust the budget a real failure needs.
//
// It posts nothing. A relaunch on a fixed cycle is not news each time it
// happens: the standing *-outage marker is the one statement that the machine
// is waiting, and it stays current on its own (SC-2851 — this path used to
// leave one note per attempt, which is how a weekend produced hundreds).
func (r StageRetry) relaunchOutage(pmKey string, stage BoardStage, logger zerolog.Logger) bool {
	launched, err := r.Relaunch(pmKey, stage)
	if err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: outage relaunch failed, leaving the outage marker in place")
		return false
	}
	if !launched {
		// The relaunch was refused — nothing started, so this is not a re-drive to
		// report. Leave the standing *-outage marker in place for the next reconcile
		// tick rather than logging a launch that never happened (SC-2989).
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: outage relaunch refused (nothing started); leaving the outage marker in place")
		return false
	}
	logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
		Msg("board retry: stage relaunched after substrate outage (budget untouched)")
	return true
}

// relaunchSilenceReap re-drives a stage the zombie sweep reaped for silence
// (no hook event AND no transcript output past the idle budget) rather than a
// genuine crash. Sibling of relaunchOutage: it deliberately never reads or
// bumps the attempt counter, because the reap was the machine's own
// judgement, not a stage failure — charging the budget for it would let a
// misjudged stop cost the ticket its ability to recover on its own, exactly
// the harm SC-2447 reports. The caller has already posted the marker
// explaining what was observed and why, so this only relaunches.
func (r StageRetry) relaunchSilenceReap(pmKey string, stage BoardStage, logger zerolog.Logger) bool {
	launched, err := r.Relaunch(pmKey, stage)
	if err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: silence-reap relaunch failed, leaving the card as failed")
		return false
	}
	if !launched {
		// A refused relaunch started nothing — report unhandled and leave the card
		// as the refusal routed it, rather than logging a re-drive that never
		// happened (SC-2989).
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: silence-reap relaunch refused (nothing started); leaving the card as failed")
		return false
	}
	logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
		Msg("board retry: stage relaunched after a silence reap (budget untouched)")
	return true
}

// recordedOutage reports whether the stage recorded ExitOutage. It is what the
// live exit handler consults BEFORE it composes any marker, so an outage is
// routed to its own *-outage marker instead of a *-failed one. False when retry
// is unwired, so an unconfigured StageRetry keeps prior behaviour.
func (r StageRetry) recordedOutage(pmKey string, stage BoardStage) bool {
	if !r.enabled() || r.Outcome == nil {
		return false
	}
	outcome, recorded := r.Outcome(pmKey, stage)
	return recorded && outcome == ExitOutage
}

// note records the automatic retry on the ticket so the trail shows why the
// stage started again. It is a plain comment on purpose — a [human:*] header
// would be classified as a stage marker and move the card.
func (r StageRetry) note(ctx context.Context, pmKey string, stage BoardStage, attempt int, outcome StageExit, recorded bool, commenter tracker.Commenter, daemonID string, logger zerolog.Logger) {
	if commenter == nil {
		return
	}
	reason := "the agent exited without recording an outcome"
	if recorded {
		reason = "the stage recorded exit: " + string(outcome)
	}
	body := "Automatic retry " + strconv.Itoa(attempt) + "/" + strconv.Itoa(r.max()) + " of the " +
		string(stage) + " stage — " + reason + "."
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Msg("board retry: cannot post the retry note")
	}
}
