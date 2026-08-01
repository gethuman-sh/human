package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// retryRecorder captures what a retry attempt did.
type retryRecorder struct {
	relaunched []BoardStage
	comments   []string
	attempts   int
	attemptErr error
	resets     []BoardStage
	relaunchEr error
}

func (r *retryRecorder) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, nil
}

func (r *retryRecorder) AddComment(_ context.Context, _, body string) (*tracker.Comment, error) {
	r.comments = append(r.comments, body)
	return &tracker.Comment{}, nil
}

func (r *retryRecorder) policy(outcome string, recorded bool) StageRetry {
	return StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (string, bool) { return outcome, recorded },
		Attempts: func(string, BoardStage) (int, error) { r.attempts++; return r.attempts, r.attemptErr },
		Reset:    func(_ string, s BoardStage) { r.resets = append(r.resets, s) },
		Relaunch: func(_ string, s BoardStage) error {
			if r.relaunchEr != nil {
				return r.relaunchEr
			}
			r.relaunched = append(r.relaunched, s)
			return nil
		},
	}
}

func TestClassifyRelaunch_ExitClasses(t *testing.T) {
	// An agent that died before recording anything is the crash an automatic
	// retry exists to absorb — bounded, so a vanished agent cannot loop forever.
	require.Equal(t, relaunchBounded, classifyRelaunch("", false))
	require.Equal(t, relaunchBounded, classifyRelaunch(ExitRetryable, true))

	// A substrate outage is its own kind: relaunched, but on the uncharged
	// backoff path rather than against the bounded budget (SC-2307).
	require.Equal(t, relaunchOutage, classifyRelaunch(ExitOutage, true))

	// A stage that reached a deliberate conclusion must not be looped on.
	require.Equal(t, relaunchNone, classifyRelaunch(ExitNeedsHumanWork, true))
	require.Equal(t, relaunchNone, classifyRelaunch(ExitNeedsInput, true))
	require.Equal(t, relaunchNone, classifyRelaunch(ExitDone, true))

	// An outcome we do not recognise is deliberate output we cannot parse —
	// retrying it would burn attempts to no purpose.
	require.Equal(t, relaunchNone, classifyRelaunch("something-else", true))
}

func TestTryRelaunch_RetryableStageIsRelaunchedAndNoted(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitRetryable, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "daemon-1", zerolog.Nop())

	require.True(t, ok)
	require.Equal(t, []BoardStage{BoardImplementation}, rec.relaunched)
	require.Len(t, rec.comments, 1)
	require.Contains(t, rec.comments[0], "Automatic retry 1/2")
	require.Contains(t, rec.comments[0], "implementation")
}

// The note must never be classified as a stage marker, or the board would move
// the card on a comment that is only an explanation.
func TestTryRelaunch_NoteIsNotAStageMarker(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy("", false)

	policy.tryRelaunch(context.Background(), "SC-1", BoardPlanning, rec, "daemon-1", zerolog.Nop())

	require.Len(t, rec.comments, 1)
	_, _, classified := ClassifyMarker(rec.comments[0])
	require.False(t, classified, "the retry note must not be a stage marker: %q", rec.comments[0])
}

func TestTryRelaunch_TerminalExitIsLeftForAHuman(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitNeedsHumanWork, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "d", zerolog.Nop())

	require.False(t, ok)
	require.Empty(t, rec.relaunched)
	require.Empty(t, rec.comments, "a card left for a human gets no retry note")
}

// The cap is what stops a crash-looping stage from burning tokens forever.
func TestTryRelaunch_StopsAtTheAttemptCap(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitRetryable, true)
	ctx := context.Background()

	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardImplementation, rec, "d", zerolog.Nop()))
	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardImplementation, rec, "d", zerolog.Nop()))
	require.False(t, policy.tryRelaunch(ctx, "SC-1", BoardImplementation, rec, "d", zerolog.Nop()),
		"the third attempt exceeds Max and must fall through to the human path")

	require.Len(t, rec.relaunched, 2)
}

// An outage is relaunched but must NEVER touch the attempt budget: it retries
// indefinitely on the reconcile backoff until the substrate returns (SC-2307).
func TestTryRelaunch_OutageDoesNotChargeBudget(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitOutage, true)
	ctx := context.Background()

	// Many outage relaunches in a row: none may consult or bump the counter.
	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardImplementation, rec, "daemon-1", zerolog.Nop()))
	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardImplementation, rec, "daemon-1", zerolog.Nop()))
	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardImplementation, rec, "daemon-1", zerolog.Nop()))

	require.Len(t, rec.relaunched, 3, "every outage attempt relaunches")
	require.Zero(t, rec.attempts, "an outage must never read or charge the attempt budget")
	require.Len(t, rec.comments, 3, "each outage relaunch posts its uncharged-note")
	require.Contains(t, rec.comments[0], "not charged against the retry budget")
	_, _, classified := ClassifyMarker(rec.comments[0])
	require.False(t, classified, "the outage note must not be a stage marker: %q", rec.comments[0])
}

// An outage relaunch with no commenter (the reconcile path) still relaunches and
// still leaves the budget untouched — the note is best-effort.
func TestTryRelaunch_OutageWithoutCommenterStillRelaunches(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitOutage, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardVerification, nil, "d", zerolog.Nop())

	require.True(t, ok)
	require.Equal(t, []BoardStage{BoardVerification}, rec.relaunched)
	require.Zero(t, rec.attempts)
	require.Empty(t, rec.comments)
}

// A failed outage relaunch reports unhandled so the outage marker stays in place
// for the next reconcile tick — and still charges nothing.
func TestTryRelaunch_OutageFailedRelaunchReportsUnhandled(t *testing.T) {
	rec := &retryRecorder{relaunchEr: errors.New("transition refused")}
	policy := rec.policy(ExitOutage, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "d", zerolog.Nop())

	require.False(t, ok)
	require.Empty(t, rec.relaunched)
	require.Zero(t, rec.attempts)
}

// recordedOutage is the live handler's routing test: true only for a recorded
// ExitOutage, false for every other exit and for an unwired policy.
func TestRecordedOutage(t *testing.T) {
	rec := &retryRecorder{}
	require.True(t, rec.policy(ExitOutage, true).recordedOutage("SC-1", BoardImplementation))
	require.False(t, rec.policy(ExitRetryable, true).recordedOutage("SC-1", BoardImplementation))
	require.False(t, rec.policy(ExitOutage, false).recordedOutage("SC-1", BoardImplementation))
	require.False(t, StageRetry{}.recordedOutage("SC-1", BoardImplementation))
}

// Without a trustworthy count an automatic relaunch could loop, so a counter
// failure falls back to the human path rather than guessing.
func TestTryRelaunch_UnreadableCountFallsBackToAHuman(t *testing.T) {
	rec := &retryRecorder{attemptErr: errors.New("state unavailable")}
	policy := rec.policy(ExitRetryable, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "d", zerolog.Nop())

	require.False(t, ok)
	require.Empty(t, rec.relaunched)
}

// A relaunch that cannot be issued must leave the card failed, not report the
// failure as handled — otherwise the card would look retried and sit idle.
func TestTryRelaunch_FailedRelaunchReportsUnhandled(t *testing.T) {
	rec := &retryRecorder{relaunchEr: errors.New("transition refused")}
	policy := rec.policy(ExitRetryable, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "d", zerolog.Nop())

	require.False(t, ok)
	require.Empty(t, rec.relaunched)
}

// An unconfigured policy must leave the previous behaviour exactly as it was.
func TestTryRelaunch_DisabledWhenUnwired(t *testing.T) {
	var policy StageRetry
	rec := &retryRecorder{}

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "d", zerolog.Nop())

	require.False(t, ok)
	require.Empty(t, rec.comments)
	require.NotPanics(t, func() { policy.reset("SC-1", BoardImplementation) })
}

func TestStageRetry_MaxDefaultsWhenUnset(t *testing.T) {
	require.Equal(t, DefaultStageRetries, StageRetry{}.max())
	require.Equal(t, 5, StageRetry{Max: 5}.max())
}
