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
	refused    bool // Relaunch reports launched=false, err=nil — a gate refusal
	uncounts   int  // times the charged attempt was rolled back
}

func (r *retryRecorder) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, nil
}

func (r *retryRecorder) AddComment(_ context.Context, _, body string) (*tracker.Comment, error) {
	r.comments = append(r.comments, body)
	return &tracker.Comment{}, nil
}

func (r *retryRecorder) policy(outcome StageExit, recorded bool) StageRetry {
	return StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return outcome, recorded },
		Attempts: func(string, BoardStage) (int, error) { r.attempts++; return r.attempts, r.attemptErr },
		Reset:    func(_ string, s BoardStage) { r.resets = append(r.resets, s) },
		Relaunch: func(_ string, s BoardStage) (bool, error) {
			if r.relaunchEr != nil {
				return false, r.relaunchEr
			}
			if r.refused {
				return false, nil
			}
			r.relaunched = append(r.relaunched, s)
			return true, nil
		},
		Uncount: func(_ string, _ BoardStage) { r.uncounts++; r.attempts-- },
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

	// "done" is the contradiction: this classifier only runs on a stage already
	// judged failed for finishing without its done-marker, so the agent claims
	// success the board has no evidence of. Markers advance cards, so the stage
	// is incomplete and gets the same bounded relaunch as any other incomplete
	// stage — it must NOT be filed alongside needs-human-work, which stranded
	// planning runs that had a good plan and only missed the marker.
	require.Equal(t, relaunchBounded, classifyRelaunch(ExitDone, true))

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

// A relaunch the transition engine REFUSES (the plan gate: nothing started) is
// not a launch — it must cost no attempt and post no "Automatic retry" note
// (SC-2989). Before the fix the attempt was charged and a false note posted.
func TestTryRelaunch_RefusedRelaunchSpendsNoAttempt(t *testing.T) {
	rec := &retryRecorder{refused: true}
	policy := rec.policy(ExitRetryable, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, rec, "d", zerolog.Nop())

	require.False(t, ok, "a refused relaunch is not handled")
	require.Empty(t, rec.relaunched, "nothing was launched")
	require.Empty(t, rec.comments, "a refused relaunch posts no Automatic-retry note")
	require.Zero(t, rec.attempts, "the charged attempt is rolled back to zero")
	require.Equal(t, 1, rec.uncounts, "the attempt is rolled back exactly once")
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
	// Say it once: the standing *-outage marker is the statement that the machine
	// is waiting. A note per relaunch is how a weekend-long outage buried the
	// ticket under hundreds of identical comments (SC-2851).
	require.Empty(t, rec.comments, "a relaunch on a fixed cycle is not news each time it happens")
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

// The end-to-end shape of the stranded-planning bug: an agent attaches its plan,
// records exit "done", and exits 0 without posting the stage's done-marker. The
// board judges that a failure (no marker), and the recorded "done" must not stop
// the relaunch that recovers it.
func TestTryRelaunch_DoneWithoutTheDoneMarkerIsRelaunched(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitDone, true)

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardPlanning, rec, "daemon-1", zerolog.Nop())

	require.True(t, ok, "the failure is absorbed by a relaunch rather than left red for a human")
	require.Equal(t, []BoardStage{BoardPlanning}, rec.relaunched)
}

// The relaunch stays bounded: a stage that keeps claiming done without ever
// posting its marker reaches a human instead of looping.
func TestTryRelaunch_DoneWithoutTheDoneMarkerStopsAtTheAttemptCap(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitDone, true)
	ctx := context.Background()

	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardPlanning, rec, "d", zerolog.Nop()))
	require.True(t, policy.tryRelaunch(ctx, "SC-1", BoardPlanning, rec, "d", zerolog.Nop()))
	require.False(t, policy.tryRelaunch(ctx, "SC-1", BoardPlanning, rec, "d", zerolog.Nop()),
		"a stage that never posts its marker must reach a human, not loop")

	require.Len(t, rec.relaunched, 2)
}
