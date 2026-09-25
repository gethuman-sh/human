package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// sc3508Thread is the marker sequence the SC-3508 card actually carried: a
// fixer round killed mid-run, a second round that fixed the bundle, and the CI
// failure message the card then wore while its PR was green and mergeable.
func sc3508Thread(base time.Time) []tracker.Comment {
	return []tracker.Comment{
		cmt("[human:deploy-fix-started]\nCI checks failed on the pull request (failing: frontend-test)\npr: https://github.com/o/r/pull/408\nnumber: 408\nbranch: autofix/sc-3508", base),
		cmt("[human:deploy-failed]\nStuck in done: no terminal marker and no live agent — needs attention\npr: https://github.com/o/r/pull/408", base.Add(5*time.Minute)),
		cmt("[human:deploy-fix-started]\nCI checks failed on the pull request (failing: frontend-test)\npr: https://github.com/o/r/pull/408\nnumber: 408\nbranch: autofix/sc-3508", base.Add(10*time.Minute)),
		cmt("[human:deploy-failed]\nCI checks failed on the pull request (failing: frontend-test) — fix the failing checks, then re-run Deploy\npr: https://github.com/o/r/pull/408", base.Add(20*time.Minute)),
	}
}

// resetRedriveBackoff clears the package-level pacing state before a test runs.
// The backoff key is the card key plus the PR's head SHA (SC-3640 round 2 —
// keying on the failure marker instead let a re-drive that starts but does not
// merge, which posts its own fresh failure, reset the pacing on every cycle),
// and every test here shares the stub head "sha1" unless it overrides
// ShippableProbe: without this reset, a test that leaves a pending next-attempt
// time (a refusal never clears it) makes the next test's re-drive look not-due
// and the assertion fail on ordering alone. The sibling pass resets the same
// way (board_reconcile_failed_test.go).
func resetRedriveBackoff(t *testing.T) {
	t.Helper()
	shippableRedriveBackoff.reset()
	t.Cleanup(shippableRedriveBackoff.reset)
}

// shippableDeps builds the ReconcileDeps for the shippable re-drive arm alone:
// a stub ShippableProbe reporting a fixed head SHA, a RetryDeploy that records
// every pmKey it was called with, empty live agents (nobody home) and a
// reachable branch — the neutral case every narrowing test starts from and
// overrides one field of.
func shippableDeps(shippable bool, probeErr error, redriven *[]string) ReconcileDeps {
	return ReconcileDeps{
		MergedProbe: func(context.Context, string) (bool, error) { return false, nil },
		PostDeployed: func(context.Context, string, string) error {
			return errors.New("must not post [human:deployed] on an unmerged PR")
		},
		ShippableProbe: func(context.Context, string) (bool, string, error) { return shippable, "sha1", probeErr },
		RetryDeploy: func(pmKey string) (bool, error) {
			*redriven = append(*redriven, pmKey)
			return true, nil
		},
		LiveAgents: liveAgents(),
		Reachable:  alwaysReachable,
		Logger:     zerolog.Nop(),
	}
}

func cardWithThread(thread []tracker.Comment) []ReconcileCard {
	return []ReconcileCard{{Key: "SC-1", Comments: thread}}
}

// SC-3640: the SC-3508 shape — a red deploy-failed card whose PR has since
// become mergeable with every check green — is re-driven rather than left red.
func TestReconcileShippedFailures_SC3508SequenceRedrivesWhenThePRIsGreen(t *testing.T) {
	resetRedriveBackoff(t)
	thread := sc3508Thread(time.Now().Add(-time.Hour))
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)
	posted := false
	deps.PostDeployed = func(context.Context, string, string) error { posted = true; return nil }

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"SC-1"}, redriven)
	assert.False(t, posted, "a re-drive is not a merge — no [human:deployed] marker")
}

// The re-drive posts [human:pr-review-started] (through RetryDeploy, which is
// the same in-place retry the board's Retry-deploy gesture issues); once that
// marker lands the card is no longer red.
func TestReconcileShippedFailures_RedriveClearsTheRed(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := sc3508Thread(base)
	thread = append(thread, cmt(prReviewStartedBody("https://github.com/o/r/pull/408", 408, "autofix/sc-3508"), base.Add(21*time.Minute)))

	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardRunning, card.State, "the card is no longer red once the re-drive's own started marker lands")
}

func TestReconcileShippedFailures_NotShippablePRStaysRed(t *testing.T) {
	resetRedriveBackoff(t)
	thread := sc3508Thread(time.Now().Add(-time.Hour))
	var redriven []string
	deps := shippableDeps(false, nil, &redriven)

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven)
}

func TestReconcileShippedFailures_UnreadableShippableStateStaysRed(t *testing.T) {
	resetRedriveBackoff(t)
	thread := sc3508Thread(time.Now().Add(-time.Hour))
	var redriven []string
	deps := shippableDeps(false, errors.New("token expired"), &redriven)

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven, "an unreadable state must never be read as shippable")
}

func TestReconcileShippedFailures_LiveDoneStageAgentSpared(t *testing.T) {
	resetRedriveBackoff(t)
	thread := sc3508Thread(time.Now().Add(-time.Hour))
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)
	deps.LiveAgents = liveAgents(agentNameFor("SC-1", deployFixAgentStage))

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven, "a live done-stage agent owns this card; the re-drive must not race it")
}

func TestReconcileShippedFailures_DeployRunInFlightSpared(t *testing.T) {
	resetRedriveBackoff(t)
	thread := sc3508Thread(time.Now().Add(-time.Hour))
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)
	deps.DeployRun = func(string) (time.Time, bool) { return time.Now(), true }

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven, "a deploy running in this process registers no agent, so DeployRun is the second half of the same liveness question")
}

// Unusable liveness (a nil lister, or a failed lookup) spares only the
// re-drive arm; the merged arm needs no liveness at all and must still clear.
func TestReconcileShippedFailures_UnusableLivenessSparesTheRedriveOnly(t *testing.T) {
	resetRedriveBackoff(t)
	thread := []tracker.Comment{
		cmt("[human:deploy-failed]\nmerge conflict on main\npr: https://github.com/o/r/pull/7", time.Unix(1, 0)),
	}
	var redriven []string
	posted := false
	deps := shippableDeps(true, nil, &redriven)
	deps.LiveAgents = nil
	deps.MergedProbe = func(context.Context, string) (bool, error) { return true, nil }
	deps.PostDeployed = func(context.Context, string, string) error { posted = true; return nil }

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 1, n)
	assert.True(t, posted)
	assert.Empty(t, redriven)
}

// A [human:pr-review-failed] card derives to (done, failed) too, and it is a
// verdict about the change's content — green CI does not answer it.
func TestReconcileShippedFailures_PRReviewFailedIsNotRedriven(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: autofix/sc-3508", base),
		cmt(PRReviewFailedHeader+"\nreason: the change regresses X\npr: https://github.com/o/r/pull/408", base.Add(time.Minute)),
	}
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven)
}

// Waiting on a person is the one state the machine may not resolve.
func TestReconcileShippedFailures_OpenDecisionSpared(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := sc3508Thread(base)
	optionsAt := base.Add(21 * time.Minute)
	block := "[human:options]\nstage: implementation\ncontext: a genuine fork\n1: option one\n2: option two"
	thread = append(thread, cmt(block, optionsAt))
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven)
}

// The re-drive PUSHES the branch (openDraftPR); a machine that cannot resolve
// it would turn a stale red into a fresh push failure (SC-652).
func TestReconcileShippedFailures_UnreachableBranchLeftForAnotherMachine(t *testing.T) {
	resetRedriveBackoff(t)
	thread := sc3508Thread(time.Now().Add(-time.Hour))
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)
	deps.Reachable = neverReachable

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 0, n)
	assert.Empty(t, redriven)
}

// A failure inside FailedRecoveryGrace is left to the live exit path; past it,
// the durable re-drive picks it up.
func TestReconcileShippedFailures_FailureInsideTheGraceLeftToTheLivePath(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := sc3508Thread(base)
	failedAt := base.Add(20 * time.Minute)
	var redriven []string
	deps := shippableDeps(true, nil, &redriven)

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, failedAt.Add(time.Minute))
	assert.Equal(t, 0, n)
	assert.Empty(t, redriven, "a failure 1 minute old is left to the live path")

	n = reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, failedAt.Add(6*time.Minute))
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"SC-1"}, redriven, "past FailedRecoveryGrace the durable pass acts")
}

// A refusal that started nothing costs no immediate retry — the backoff keeps
// the next try off the tick.
func TestReconcileShippedFailures_RedriveBacksOffOnARefusal(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := sc3508Thread(base)
	var calls int
	deps := shippableDeps(true, nil, &[]string{})
	deps.RetryDeploy = func(string) (bool, error) { calls++; return false, nil }

	now := base.Add(30 * time.Minute)
	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, now)
	assert.Equal(t, 0, n)
	assert.Equal(t, 1, calls)

	n = reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, now.Add(30*time.Second))
	assert.Equal(t, 0, n)
	assert.Equal(t, 1, calls, "the backoff must not let a refusal be retried on the very next tick")
}

// SC-3640 round 2: a re-drive that STARTS but does not merge posts its own
// fresh [human:deploy-failed] — that is the normal shape of "still not
// shippable enough to actually land" — and pacing on that marker's timestamp
// (the original shape) built a brand new backoff key every cycle, so the
// pass re-drove on every tick forever rather than backing off. Simulates 40
// such cycles, each appending a fresh deploy-failed on the SAME PR head more
// than FailedRecoveryGrace before the next check, and asserts the re-drive
// count is bounded by the backoff schedule rather than growing with the tick
// count.
func TestReconcileShippedFailures_RedriveBacksOffAcrossRepeatedFailuresOnTheSameHead(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := sc3508Thread(base)
	var calls int
	deps := shippableDeps(true, nil, &[]string{})
	deps.RetryDeploy = func(string) (bool, error) { calls++; return true, nil }

	now := base.Add(30 * time.Minute)
	const cycles = 40
	for i := 0; i < cycles; i++ {
		thread = append(thread, cmt(
			"[human:deploy-failed]\nCI checks failed on the pull request (failing: frontend-test) — fix the failing checks, then re-run Deploy\npr: https://github.com/o/r/pull/408",
			now))
		now = now.Add(6 * time.Minute) // past FailedRecoveryGrace before the next check
		reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, now)
	}

	assert.Less(t, calls, cycles/2,
		"the backoff on a stable head must bound re-drives well under one per cycle over 4 simulated hours")
}

// SC-3640 round 2: a head that has genuinely moved — the PR got a new commit —
// is new evidence and earns a fresh attempt even while the PREVIOUS head's
// backoff has not elapsed; only a head that has NOT moved is paced.
func TestReconcileShippedFailures_NewHeadGetsAFreshAttempt(t *testing.T) {
	resetRedriveBackoff(t)
	base := time.Now().Add(-time.Hour)
	thread := sc3508Thread(base)
	var heads []string
	head := "aaa111"
	deps := shippableDeps(true, nil, &[]string{})
	deps.ShippableProbe = func(context.Context, string) (bool, string, error) { return true, head, nil }
	deps.RetryDeploy = func(string) (bool, error) { heads = append(heads, head); return true, nil }

	now := base.Add(30 * time.Minute)
	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, now)
	assert.Equal(t, 1, n)

	// Same head, moments later: the backoff blocks it.
	n = reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, now.Add(30*time.Second))
	assert.Equal(t, 0, n)
	assert.Equal(t, []string{"aaa111"}, heads)

	// The PR gets a new commit: the head moves, and the re-drive fires even
	// though "aaa111"'s backoff has not elapsed.
	head = "bbb222"
	n = reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, now.Add(31*time.Second))
	assert.Equal(t, 1, n, "a head that has genuinely moved is not paced by the previous head's backoff")
	assert.Equal(t, []string{"aaa111", "bbb222"}, heads)
}

// A merged PR still clears with the old, merged-only behaviour when the
// shippable arm is entirely disabled (both deps nil).
func TestReconcileShippedFailures_MergedStillClearsWithNoShippableProbe(t *testing.T) {
	resetRedriveBackoff(t)
	thread := []tracker.Comment{
		cmt("[human:deploy-failed]\nmerge conflict on main\npr: https://github.com/o/r/pull/7", time.Unix(1, 0)),
	}
	posted := false
	deps := ReconcileDeps{
		MergedProbe:  func(context.Context, string) (bool, error) { return true, nil },
		PostDeployed: func(context.Context, string, string) error { posted = true; return nil },
	}

	n := reconcileShippedFailures(context.Background(), ownWork(cardWithThread(thread)), deps, time.Now())

	assert.Equal(t, 1, n)
	assert.True(t, posted)
}

func shippableState(mergeable bool, checks ...forge.CheckResult) *forge.PullRequestState {
	return &forge.PullRequestState{Mergeable: mergeable, Checks: checks}
}

func TestPullRequestShippable_mergeableAndAllGreen(t *testing.T) {
	state := shippableState(true,
		forge.CheckResult{Name: "build", Conclusion: forge.ChecksPassing},
		forge.CheckResult{Name: "frontend-test", Conclusion: forge.ChecksPassing})
	assert.True(t, PullRequestShippable(state))
}

func TestPullRequestShippable_pendingCheckIsNotAnAnswer(t *testing.T) {
	state := shippableState(true,
		forge.CheckResult{Name: "build", Conclusion: forge.ChecksPending},
		forge.CheckResult{Name: "frontend-test", Conclusion: forge.ChecksPassing})
	assert.False(t, PullRequestShippable(state))
}

func TestPullRequestShippable_failingCheck(t *testing.T) {
	state := shippableState(true, forge.CheckResult{Name: "frontend-test", Conclusion: forge.ChecksFailing})
	assert.False(t, PullRequestShippable(state))
}

func TestPullRequestShippable_notMergeable(t *testing.T) {
	state := shippableState(false, forge.CheckResult{Name: "build", Conclusion: forge.ChecksPassing})
	assert.False(t, PullRequestShippable(state))
}

func TestPullRequestShippable_noChecksIsUnknown(t *testing.T) {
	state := shippableState(true)
	assert.False(t, PullRequestShippable(state))
}

func TestPullRequestShippable_nilState(t *testing.T) {
	assert.False(t, PullRequestShippable(nil))
}
