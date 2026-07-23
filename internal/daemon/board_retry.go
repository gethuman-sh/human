package daemon

import (
	"context"
	"strconv"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// Exit classes an agent records in its stage report before returning. They are
// the vocabulary of the prompts' exit contract; the board only has to tell
// "another attempt could plausibly fix this" from "it could not".
const (
	ExitRetryable      = "retryable"
	ExitNeedsInput     = "needs-input"
	ExitNeedsHumanWork = "needs-human-work"
	ExitDone           = "done"
)

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
	Outcome func(pmKey string, stage BoardStage) (string, bool)
	// Attempts increments and returns how many times this stage has been
	// relaunched automatically.
	Attempts func(pmKey string, stage BoardStage) (int, error)
	// Reset clears the attempt count. Called when the stage finishes cleanly,
	// so a later, unrelated failure gets a full budget instead of inheriting a
	// spent one.
	Reset func(pmKey string, stage BoardStage)
	// Relaunch issues the in-place retry transition for the stage.
	Relaunch func(pmKey string, stage BoardStage) error
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

// mayRelaunch decides from the recorded exit class alone whether another
// attempt is warranted.
//
// An unrecorded outcome counts as retryable: the agent died before it could
// write one, which is exactly the crash an automatic retry exists to absorb.
// That is also the safe direction, because the attempt cap bounds it — whereas
// treating a vanished agent as terminal is how a card ends up red with nobody
// having looked at it. An outcome we do not recognise is NOT retried: the agent
// said something deliberate, and looping on a sentence we cannot parse would
// burn attempts to no purpose.
func mayRelaunch(outcome string, recorded bool) bool {
	if !recorded {
		return true
	}
	return outcome == ExitRetryable
}

// reset clears a stage's attempt count after a clean finish.
func (r StageRetry) reset(pmKey string, stage BoardStage) {
	if r.Reset != nil {
		r.Reset(pmKey, stage)
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
	if !mayRelaunch(outcome, recorded) {
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Str("exit", outcome).
			Msg("board retry: stage exit is not retryable, leaving the card for a human")
		return false
	}

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

	r.note(ctx, pmKey, stage, attempt, outcome, recorded, commenter, daemonID, logger)

	if err := r.Relaunch(pmKey, stage); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: relaunch failed, leaving the card as failed")
		return false
	}
	logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Int("attempt", attempt).
		Msg("board retry: stage relaunched automatically")
	return true
}

// note records the automatic retry on the ticket so the trail shows why the
// stage started again. It is a plain comment on purpose — a [human:*] header
// would be classified as a stage marker and move the card.
func (r StageRetry) note(ctx context.Context, pmKey string, stage BoardStage, attempt int, outcome string, recorded bool, commenter tracker.Commenter, daemonID string, logger zerolog.Logger) {
	if commenter == nil {
		return
	}
	reason := "the agent exited without recording an outcome"
	if recorded {
		reason = "the stage recorded exit: " + outcome
	}
	body := "Automatic retry " + strconv.Itoa(attempt) + "/" + strconv.Itoa(r.max()) + " of the " +
		string(stage) + " stage — " + reason + "."
	if _, err := commenter.AddComment(ctx, pmKey, StampDaemon(body, daemonID)); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Msg("board retry: cannot post the retry note")
	}
}
