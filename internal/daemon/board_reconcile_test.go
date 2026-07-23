package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// alwaysReachable is the test predicate for reconcile passes that are not
// exercising the reachability gate: every handoff branch resolves on this
// machine, matching the fixer-and-reviewer-share-a-machine invariant.
func alwaysReachable(string) bool { return true }

// An orphaned handoff — a [human:ready-for-review] with no subsequent review
// marker — is exactly the card the live fix→review chain missed on restart, so
// the reconcile pass must launch its review.
func TestReconcileOrphanedHandoffs_LaunchesReviewForOrphan(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
	}}
	var chained []string
	n := reconcileOrphanedHandoffs(cards, alwaysReachable, nil, func(pmKey string) error {
		chained = append(chained, pmKey)
		return nil
	}, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"SC-1"}, chained)
}

// A review already in flight (review-started) advances the card to the
// verification stage, so the orphan condition (furthest stage ==
// implementation) no longer holds and no second review is launched.
func TestReconcileOrphanedHandoffs_SkipsWhenReviewStarted(t *testing.T) {
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
			cmt("[human:review-started]", time.Unix(2, 0)),
		},
	}}
	called := false
	n := reconcileOrphanedHandoffs(cards, alwaysReachable, nil, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// A completed review likewise moves the card past implementation, so the
// reconcile pass leaves it alone.
func TestReconcileOrphanedHandoffs_SkipsWhenReviewComplete(t *testing.T) {
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0)),
			cmt("[human:review-complete]\nverdict: pass", time.Unix(2, 0)),
		},
	}}
	called := false
	n := reconcileOrphanedHandoffs(cards, alwaysReachable, nil, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// A still-building card (implementation-started, no handoff yet) is not an
// orphan — its state is running, not done — so it must never be reviewed.
func TestReconcileOrphanedHandoffs_SkipsRunningBuild(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))},
	}}
	called := false
	n := reconcileOrphanedHandoffs(cards, alwaysReachable, nil, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// A board-context fix leaves its branch local on the machine that produced it;
// a daemon on any other machine cannot resolve that branch, so its reconcile
// pass must NOT chain a review it could never satisfy — it leaves the handoff
// for a daemon that can reach the branch (SC-652).
func TestReconcileOrphanedHandoffs_SkipsUnreachableBranch(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: autofix/sc-1", time.Unix(1, 0))},
	}}
	unreachable := func(string) bool { return false }
	called := false
	n := reconcileOrphanedHandoffs(cards, unreachable, nil, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called)
}

// The gate probes the handoff's own branch: reconcile must hand the reachability
// predicate the exact branch parsed from the ready-for-review marker.
func TestReconcileOrphanedHandoffs_PassesHandoffBranchToProbe(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: autofix/sc-1", time.Unix(1, 0))},
	}}
	var probed string
	reachable := func(branch string) bool { probed = branch; return true }
	n := reconcileOrphanedHandoffs(cards, reachable, nil, func(string) error { return nil }, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Equal(t, "autofix/sc-1", probed)
}

// A handoff that names commits the branch does not contain (a retry that never
// pushed its work, 735) must be skipped-and-left on the durable reconcile pass —
// a periodic scan must never red a card another machine can legitimately serve.
func TestReconcileOrphanedHandoffs_SkipsPhantomCommits(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0))},
	}}
	var probedBranch string
	var probedCommits []string
	commitsPresent := func(branch string, commits []string) bool {
		probedBranch, probedCommits = branch, commits
		return false
	}
	called := false
	n := reconcileOrphanedHandoffs(cards, alwaysReachable, commitsPresent, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, called, "a phantom-commit handoff must not chain a review")
	assert.Equal(t, "feat/x", probedBranch)
	assert.Equal(t, []string{"abc123"}, probedCommits)
}

// A handoff whose named commits ARE present on the branch chains normally —
// the phantom-commit gate must not block a legitimate handoff.
func TestReconcileOrphanedHandoffs_ChainsWhenCommitsPresent(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0))},
	}}
	commitsPresent := func(string, []string) bool { return true }
	called := false
	n := reconcileOrphanedHandoffs(cards, alwaysReachable, commitsPresent, func(string) error { called = true; return nil }, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.True(t, called)
}

// This is the bug: with a fresh watcher (empty HookEventStore, no live exit
// event) nothing rescanned comments on start, so a restart-orphaned handoff
// sat forever. RunBoardReconcile's immediate startup pass must recover it
// without waiting a full interval and without any hook event.
func TestRunBoardReconcile_RecoversOrphanWithNoLiveEvent(t *testing.T) {
	lister := func(context.Context) ([]ReconcileCard, error) {
		return []ReconcileCard{{
			Key:      "SC-1",
			Comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
		}}, nil
	}
	chained := make(chan string, 1)
	chain := func(pmKey string) error { chained <- pmKey; return nil }

	ctx := t.Context()
	// A long interval proves the recovery comes from the immediate startup pass,
	// not a ticker tick.
	go RunBoardReconcile(ctx, lister, alwaysReachable, nil, nil, nil, nil, nil, chain, StageRetry{}, "", time.Hour, zerolog.Nop())

	select {
	case pmKey := <-chained:
		assert.Equal(t, "SC-1", pmKey)
	case <-time.After(2 * time.Second):
		t.Fatal("expected the startup reconcile pass to recover the orphaned handoff")
	}
}

// A lone [human:deploy-failed] whose PR was merged out-of-band (manual merge,
// no follow-up marker) must be confirmed-shipped by the forge probe: reconcile
// posts a [human:deployed] marker so the existing supersession guard clears the
// stale red. This is the 695-class bug (SC-910).
func TestReconcileShippedFailures_MergedPRClearsRed(t *testing.T) {
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:deploy-failed]\nmerge conflict on main\npr: https://github.com/o/r/pull/7", time.Unix(1, 0)),
		},
	}}
	var postedKey, postedURL string
	merged := func(_ context.Context, prURL string) (bool, error) { return true, nil }
	post := func(_ context.Context, pmKey, prURL string) error {
		postedKey, postedURL = pmKey, prURL
		return nil
	}
	n := reconcileShippedFailures(context.Background(), cards, merged, post, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Equal(t, "SC-1", postedKey)
	assert.Equal(t, "https://github.com/o/r/pull/7", postedURL)
}

// The forge reporting the PR NOT merged leaves the card red — a genuine open
// failure must not be silently cleared.
func TestReconcileShippedFailures_UnmergedPRLeftRed(t *testing.T) {
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:deploy-failed]\nmerge conflict on main\npr: https://github.com/o/r/pull/7", time.Unix(1, 0)),
		},
	}}
	posted := false
	merged := func(_ context.Context, _ string) (bool, error) { return false, nil }
	post := func(_ context.Context, _, _ string) error { posted = true; return nil }
	n := reconcileShippedFailures(context.Background(), cards, merged, post, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, posted)
}

// A failed card with no PR URL (e.g. a pre-PR deploy-failed) has nothing to
// probe and is skipped — never posts, never errors.
func TestReconcileShippedFailures_NoPRURLSkipped(t *testing.T) {
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:deploy-failed]\nno branch recorded", time.Unix(1, 0))},
	}}
	probed := false
	merged := func(_ context.Context, _ string) (bool, error) { probed = true; return true, nil }
	post := func(_ context.Context, _, _ string) error { return nil }
	n := reconcileShippedFailures(context.Background(), cards, merged, post, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.False(t, probed)
}

// nil deps disable the step (the package's "nil disables" convention).
func TestReconcileShippedFailures_NilDepsDisabled(t *testing.T) {
	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{
		cmt("[human:deploy-failed]\nx\npr: https://github.com/o/r/pull/7", time.Unix(1, 0))}}}
	assert.Equal(t, 0, reconcileShippedFailures(context.Background(), cards, nil, nil, zerolog.Nop()))
}

// liveAgents is the test lister: the set of board agent names currently running
// on this machine. An empty set models a card whose stage agent is gone.
func liveAgents(names ...string) LiveAgentLister {
	return func() ([]string, error) { return names, nil }
}

// capturingPoster records the (pmKey, body) of every *-failed marker the
// stuck-running pass posts, so a test can assert both the target and the badge
// header without a tracker.
func capturingPoster(posted *[]struct{ Key, Body string }) FailedMarkerPoster {
	return func(_ context.Context, pmKey, body string) error {
		*posted = append(*posted, struct{ Key, Body string }{pmKey, body})
		return nil
	}
}

// This is the bug (1136): a card left at [human:implementation-started] with no
// terminal marker and no live agent freezes at "being fixed" forever — a
// bug-verify NOT DONE STOPped the run without posting any board-terminal
// marker, and no reconcile pass ever red it. The durable stuck-running pass
// must red an aged running-orphan whose stage agent is not alive.
func TestReconcileStuckRunning_FailsAgedRunningOrphanWithNoAgent(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(), capturingPoster(&posted), StageRetry{}, "", now, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Len(t, posted, 1)
	assert.Equal(t, "SC-1", posted[0].Key)
	assert.True(t, strings.HasPrefix(posted[0].Body, ImplementationFailedHeader),
		"body must start with the implementation-failed header so it becomes the badge")
}

// A card younger than the grace age might be a slow-but-live run, so the pass
// must leave it alone — the grace protects genuine work from a premature red.
func TestReconcileStuckRunning_SkipsWithinGrace(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(), capturingPoster(&posted), StageRetry{}, "", now, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.Empty(t, posted)
}

// An aged card whose stage agent is still running on this machine is genuine
// slow work, not a dead-end — the liveness probe spares it.
func TestReconcileStuckRunning_SkipsWhenAgentAlive(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	live := liveAgents(agentNameFor("SC-1", BoardImplementation))
	n := reconcileStuckRunning(context.Background(), cards, live, capturingPoster(&posted), StageRetry{}, "", now, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.Empty(t, posted)
}

// A card already carrying a terminal *-failed marker is not running, so the
// pass skips it — the guarantee that once red it never double-posts.
func TestReconcileStuckRunning_SkipsNonRunning(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-2*time.Minute)),
			cmt("[human:implementation-failed]\nstuck", now.Add(-StuckRunningGrace-time.Minute)),
		},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(), capturingPoster(&posted), StageRetry{}, "", now, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.Empty(t, posted)
}

// The pass covers every running stage uniformly: a stuck verification-stage
// card (review-started, no reviewer agent) reds with the review-failed header.
func TestReconcileStuckRunning_CoversVerificationStage(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:review-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(), capturingPoster(&posted), StageRetry{}, "", now, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, ReviewFailedHeader))
}

// If liveness cannot be established (nil lister), the pass does nothing — it
// must never red a card it cannot prove is dead.
func TestReconcileStuckRunning_NilListerDisables(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt("[human:implementation-started]", now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, nil, capturingPoster(&posted), StageRetry{}, "", now, zerolog.Nop())

	assert.Equal(t, 0, n)
	assert.Empty(t, posted)
}
