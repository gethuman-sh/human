package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A needs-input exit is a claim that a person has a question to answer. With
// no open [human:options] block on the ticket the claim is empty, and the card
// used to sit red with nothing watching it (SC-4244, F3). It takes the bounded
// relaunch instead.
func TestTryRelaunch_NeedsInputWithoutAnOpenDecisionIsRelaunched(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitNeedsInput, true)
	thread := []tracker.Comment{{Body: "[human:implementation-started]", Created: time.Now()}}

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, thread, rec, "d", zerolog.Nop())

	require.True(t, ok)
	require.Equal(t, []BoardStage{BoardImplementation}, rec.relaunched)
	require.Equal(t, 1, rec.attempts, "charged like any incomplete ending")
	require.Len(t, rec.comments, 1)
	require.Contains(t, rec.comments[0], "no open decision stands on the ticket")
}

// With a real question open, the exit is exactly what it says and the card
// waits for the person.
func TestTryRelaunch_NeedsInputWithAnOpenDecisionWaits(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitNeedsInput, true)
	base := time.Now()
	thread := []tracker.Comment{
		{Body: "[human:implementation-started]", Created: base},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", Created: base.Add(time.Second)},
	}

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, thread, rec, "d", zerolog.Nop())

	require.False(t, ok)
	require.Empty(t, rec.relaunched)
	require.Zero(t, rec.attempts)
}

// An answered decision is not an open one: the hold it may carry is the queued
// pass's business, but a later needs-input with no new question is empty.
func TestTryRelaunch_AnsweredDecisionDoesNotCountAsOpen(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy(ExitNeedsInput, true)
	base := time.Now()
	thread := []tracker.Comment{
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", Created: base},
		{Body: "[human:option-chosen] 1: a\nstage: implementation", Created: base.Add(time.Second)},
		{Body: "[human:implementation-started]", Created: base.Add(2 * time.Second)},
	}

	require.True(t, policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, thread, rec, "d", zerolog.Nop()))
	require.Equal(t, []BoardStage{BoardImplementation}, rec.relaunched)
}

// The refund follows launched, not err: an errored relaunch that started
// nothing spends no attempt (two of them used to exhaust the budget), while a
// relaunch that DID start an agent and only lost its started marker stays
// charged and counts as handled — the live run is what is watched now (F4).
func TestRelaunchBounded_ChargesOnlyWhatLaunched(t *testing.T) {
	for _, tc := range []struct {
		name     string
		launched bool
		err      error
		handled  bool
		attempts int
		uncounts int
	}{
		{"errored, nothing started", false, errors.New("docker: no such image"), false, 0, 1},
		{"refused, nothing started", false, nil, false, 0, 1},
		{"started, marker lost", true, errors.New("posting started marker"), true, 1, 0},
		{"started", true, nil, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &retryRecorder{}
			policy := rec.policy(ExitRetryable, true)
			policy.Relaunch = func(string, BoardStage) (bool, error) { return tc.launched, tc.err }

			ok := policy.tryRelaunch(context.Background(), "SC-1", BoardPlanning, nil, rec, "d", zerolog.Nop())

			require.Equal(t, tc.handled, ok)
			require.Equal(t, tc.attempts, rec.attempts, "attempts left charged")
			require.Equal(t, tc.uncounts, rec.uncounts)
		})
	}
}

// A failure for a stage the card has already moved past describes a finished
// round: the review verdict was posted and the PR loop started after the
// verification failure was recorded (SC-3853). Relaunching it would be
// rejected as a non-advance and charged; it is retired instead, uncharged.
func TestTryRelaunch_FailureForAStageTheCardHasLeftIsRetired(t *testing.T) {
	rec := &retryRecorder{}
	policy := rec.policy("", false)
	base := time.Now()
	thread := []tracker.Comment{
		{Body: "[human:review-failed]\nreason: reddened by reconcileStuckRunning", Created: base},
		{Body: "[human:review-complete]\nverdict: pass", Created: base.Add(time.Minute)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", Created: base.Add(2 * time.Minute)},
	}

	ok := policy.tryRelaunch(context.Background(), "SC-1", BoardVerification, thread, rec, "d", zerolog.Nop())

	require.True(t, ok, "handled by leaving it")
	require.Empty(t, rec.relaunched)
	require.Zero(t, rec.attempts, "a stale failure charges nothing")
	require.Empty(t, rec.comments)
}

// staleFailure only compares ranked placements: backlog and the failed stage
// itself rank at or below the failed stage and never qualify as stale, only a
// placement genuinely past it does. staleFailure calls DeriveBoardCard with
// CategoryUnstarted/isIdea hardcoded false, so a hidden (closed) or idea
// placement can never arise from its comments-only input in the first place —
// that guarantee is structural, at the call site, not something this test can
// exercise by varying the thread.
func TestStaleFailure_OnlyPlacementsPastTheFailedStageQualify(t *testing.T) {
	require.False(t, staleFailure(nil, BoardPlanning), "an empty thread derives to backlog, below any stage")
	require.False(t, staleFailure([]tracker.Comment{{Body: "[human:planning-failed]\nreason: r", Created: time.Now()}}, BoardPlanning))
	require.True(t, staleFailure([]tracker.Comment{{Body: "[human:implementation-started]", Created: time.Now()}}, BoardPlanning))
}

// A failing verdict leaves the card at verification/done while the rework
// build runs implementation again in place. That card is "ahead" of the
// failed stage by rank, but the rework dispatch relaunches exactly this shape:
// a crash of the rework agent must relaunch, not be retired as stale.
func TestTryRelaunch_reworkBuildCrashIsNotStale(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	thread := []tracker.Comment{
		{Body: "[human:implementation-started]", Created: base},
		{Body: "[human:ready-for-review]\nbranch: feat/x", Created: base.Add(time.Minute)},
		{Body: "[human:review-started]", Created: base.Add(2 * time.Minute)},
		{Body: "[human:review-complete]\nverdict: fail", Created: base.Add(3 * time.Minute)},
		{Body: "[human:implementation-started]", Created: base.Add(4 * time.Minute)},
		{Body: "[human:implementation-failed]\nreason: r", Created: base.Add(5 * time.Minute)},
	}
	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)
	require.Equal(t, BoardVerification, card.Stage, "precondition: the rework leaves the card at verification")
	require.True(t, isReworkTransition(BoardImplementation, card), "precondition: this is the rework shape")
	require.False(t, staleFailure(thread, BoardImplementation))

	rec := &retryRecorder{}
	policy := rec.policy("", false)
	require.True(t, policy.tryRelaunch(context.Background(), "SC-1", BoardImplementation, thread, rec, "d", zerolog.Nop()))
	require.Equal(t, []BoardStage{BoardImplementation}, rec.relaunched)
}

// Both callers hand tryRelaunch the thread WITH the failed marker they just
// posted, because that is the thread the transition layer will re-read. A
// verification failure posted over a done/running card takes the card back to
// verification/failed, where the review retry is sanctioned; judged from the
// pre-post snapshot the same failure reads as stale and the card is left red
// where no pass reaches it (SC-5104, round 5).
func TestStaleFailure_isJudgedOnThePostedThread(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	before := []tracker.Comment{
		{Body: "[human:review-failed]\nreason: reddened by reconcileStuckRunning", Created: base},
		{Body: "[human:review-complete]\nverdict: pass", Created: base.Add(time.Minute)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", Created: base.Add(2 * time.Minute)},
	}
	require.True(t, staleFailure(before, BoardVerification), "the pre-post snapshot places the card in the done stage")

	after := append(append([]tracker.Comment{}, before...),
		tracker.Comment{Body: "[human:review-failed]\nreason: r", Created: base.Add(3 * time.Minute)})
	card := DeriveBoardCard(after, tracker.CategoryUnstarted, false)
	require.True(t, isReviewRetry(BoardVerification, card), "precondition: the transition layer would accept the relaunch")
	require.False(t, staleFailure(after, BoardVerification), "the posted thread sanctions the review retry")
}
