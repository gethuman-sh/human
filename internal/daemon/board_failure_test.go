package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/tracker"
)

// syncCommenter is a concurrency-safe commenter for the watcher's goroutines.
type syncCommenter struct {
	mu       sync.Mutex
	comments []tracker.Comment
	added    []string
	addCh    chan string
}

func (s *syncCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tracker.Comment, len(s.comments))
	copy(out, s.comments)
	return out, nil
}

func (s *syncCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	s.mu.Lock()
	s.added = append(s.added, body)
	s.mu.Unlock()
	if s.addCh != nil {
		s.addCh <- body
	}
	c := tracker.Comment{Body: body, Created: time.Now()}
	return &c, nil
}

// withInstantBoardExitRecheck removes the bounded-backoff re-read's wait so a
// test that never settles the stage (genuinely incomplete, on purpose) decides
// on the very first ListComments call — mirroring the pre-SC-1484 timing so
// these tests stay fast and deterministic. Tests that specifically exercise
// the re-read/backoff behavior (e.g. the race regression test) shrink the vars
// themselves instead, since they need at least one extra read to occur.
func withInstantBoardExitRecheck(t *testing.T) {
	t.Helper()
	oldStep, oldTries := boardExitRecheckStep, boardExitRecheckTries
	boardExitRecheckStep, boardExitRecheckTries = 0, 1
	t.Cleanup(func() { boardExitRecheckStep, boardExitRecheckTries = oldStep, oldTries })
}

// raceCommenter reproduces the read-after-write race between a reap-synthesized
// exit event and the tracker's comment thread catching up to a just-posted
// hand-off: ListComments returns a queued sequence of thread snapshots across
// successive calls, with the last snapshot sticking once the queue is drained.
type raceCommenter struct {
	mu        sync.Mutex
	snapshots [][]tracker.Comment
	call      int
	added     []string
	addCh     chan string
}

func (r *raceCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.call
	if idx >= len(r.snapshots) {
		idx = len(r.snapshots) - 1
	}
	r.call++
	out := make([]tracker.Comment, len(r.snapshots[idx]))
	copy(out, r.snapshots[idx])
	return out, nil
}

func (r *raceCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	r.mu.Lock()
	r.added = append(r.added, body)
	r.mu.Unlock()
	if r.addCh != nil {
		r.addCh <- body
	}
	c := tracker.Comment{Body: body, Created: time.Now()}
	return &c, nil
}

// SC-1484: a reap-synthesized StopFailure can fire before the just-posted
// [human:ready-for-review] hand-off is visible in the fetched comment thread —
// the classic read-after-write race. The watcher must re-read with bounded
// backoff before deciding the stage failed: the first ListComments call sees
// only the started marker, the second (and later) sees the hand-off. That must
// NOT post an implementation-failed marker, and the completed build must still
// chain into its review exactly like a clean exit.
func TestRunBoardFailureWatch_ReapAfterHandoffRechecksAndChains(t *testing.T) {
	origStep, origTries := boardExitRecheckStep, boardExitRecheckTries
	boardExitRecheckStep = 10 * time.Millisecond
	boardExitRecheckTries = 3
	t.Cleanup(func() {
		boardExitRecheckStep, boardExitRecheckTries = origStep, origTries
	})

	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &raceCommenter{
			snapshots: [][]tracker.Comment{
				{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
				{
					cmt(ImplementationStartedHeader, time.Unix(1, 0)),
					cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(2, 0)),
				},
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		// Reap-synthesized event: no SessionID, carries only name + time.
		store.Append(hookevents.Event{EventName: "StopFailure", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

		select {
		case pmKey := <-chained:
			assert.Equal(t, "SC-1", pmKey)
		case body := <-c.addCh:
			t.Fatalf("must not post a failed marker for a reap that raced the hand-off, got: %q", body)
		case <-time.After(2 * time.Second):
			t.Fatal("expected the reap-after-handoff exit to re-read and chain a review")
		}

		c.mu.Lock()
		defer c.mu.Unlock()
		assert.Empty(t, c.added, "a reap that raced a completed hand-off must post no failed marker")
	})
}

func TestRunBoardFailureWatch_PostsFailedOnIncompleteStage(t *testing.T) {
	withInstantBoardExitRecheck(t)
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			assert.Contains(t, body, PlanningFailedHeader)
		case <-time.After(2 * time.Second):
			t.Fatal("expected a failed marker to be posted")
		}
	})
}

// SC-201: board stage agents reuse the same deterministic name on every
// rebuild (agentNameFor is deterministic; the rework path, forward
// Implementation and ApplyFix all re-launch the same name). The watcher must
// handle EVERY exit of a reused name, not just the first — a name-keyed
// lifetime dedupe silently dropped second-and-later runs.
func TestRunBoardFailureWatch_ReusedNameSecondIncompleteExitPostsAgain(t *testing.T) {
	withInstantBoardExitRecheck(t)
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		// First exit of the reused name posts a failed marker.
		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})
		select {
		case body := <-c.addCh:
			assert.Contains(t, body, PlanningFailedHeader)
		case <-time.After(2 * time.Second):
			t.Fatal("expected first failed marker")
		}

		// Second exit of the SAME name (a re-run) must post AGAIN.
		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})
		select {
		case body := <-c.addCh:
			assert.Contains(t, body, PlanningFailedHeader)
		case <-time.After(2 * time.Second):
			t.Fatal("second exit of a reused agent name must post a failed marker again (SC-201)")
		}
	})
}

// SC-201: a second cleanly-finished build of the same reused name must chain
// into review again, not be swallowed by lifetime dedupe.
func TestRunBoardFailureWatch_ReusedNameSecondCleanBuildChainsAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 2)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})
		select {
		case pmKey := <-chained:
			assert.Equal(t, "SC-1", pmKey)
		case <-time.After(2 * time.Second):
			t.Fatal("expected first build to chain a review")
		}

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})
		select {
		case pmKey := <-chained:
			assert.Equal(t, "SC-1", pmKey)
		case <-time.After(2 * time.Second):
			t.Fatal("second clean build of a reused agent name must chain a review again (SC-201)")
		}
	})
}

func TestRunBoardFailureWatch_NoPostWhenStageDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

		select {
		case <-c.addCh:
			t.Fatal("must not post failed marker when stage completed cleanly")
		case <-time.After(500 * time.Millisecond):
		}

		c.mu.Lock()
		require.Empty(t, c.added)
		c.mu.Unlock()
	})
}

func TestHandleBoardAgentExit_MalformedName(t *testing.T) {
	var called bool
	commenterFor := func() (tracker.Commenter, error) {
		called = true
		return &syncCommenter{}, nil
	}
	// A name that does not parse must short-circuit before resolving a commenter.
	handleBoardAgentExit(context.Background(), "board-", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
	assert.False(t, called)
}

// TestHandleBoardAgentExit_prReviewStage_drivesLoop verifies a PR loop agent's
// exit is routed to the loop driver — not the generic stage-failure path — and
// reclaims the worktree first.
func TestHandleBoardAgentExit_prReviewStage_drivesLoop(t *testing.T) {
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var advanced []string
	var gotAgent, gotErrorType string
	advance := func(pmKey, agentName, errorType string) error {
		advanced = append(advanced, pmKey)
		gotAgent, gotErrorType = agentName, errorType
		return nil
	}
	var reclaimed string
	onHandoff := func(agentName string) { reclaimed = agentName }

	handleBoardAgentExit(context.Background(), "board-SC-1-prreview", "crashed", "", commenterFor, nil, nil, advance, nil, alwaysReachable, nil, nil, onHandoff, StageRetry{}, nil, "", zerolog.Nop())

	assert.Equal(t, []string{"SC-1"}, advanced, "the PR loop driver must be invoked once")
	// A step that dies before recording an outcome can only be explained from its
	// artifacts, so its identity must reach the driver (SC-1892).
	assert.Equal(t, "board-SC-1-prreview", gotAgent, "the exiting run's name must reach the loop driver")
	assert.Equal(t, "crashed", gotErrorType, "the exit's error type must reach the loop driver")
	assert.Equal(t, "board-SC-1-prreview", reclaimed, "the loop step's worktree must be reclaimed")
	assert.Empty(t, c.added, "a PR loop exit must not post a generic stage-failed marker")
}

func TestHandleBoardAgentExit_CommenterError(t *testing.T) {
	commenterFor := func() (tracker.Commenter, error) {
		return nil, assertErr{}
	}
	// Must not panic when the commenter cannot be resolved.
	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
}

type assertErr struct{}

func (assertErr) Error() string { return "no commenter" }

// A clean build whose handoff names commits the branch does not contain (a retry
// that never pushed its work, 735) must fail LOUDLY on the live chain — post
// [human:implementation-failed] and NOT chain a review that would bind against
// SHAs the branch never held.
func TestHandleBoardAgentExit_PhantomCommitFailsLoudly(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }
	commitsPresent := func(string, []string) ProbeResult { return ProbeResult{Status: ProbeAbsent} }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, chain, nil, nil, nil, alwaysReachable, commitsPresent, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "a phantom-commit handoff must not chain a review")
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], ImplementationFailedHeader),
		"expected a loud implementation-failed marker, got %q", c.added[0])
	assert.Contains(t, c.added[0], "commits absent")
}

// The phantom-commit gate must not block a legitimate handoff: when the named
// commits ARE present on the branch the build chains into its review as usual.
func TestHandleBoardAgentExit_PresentCommitsChainReview(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }
	commitsPresent := func(string, []string) ProbeResult { return ProbeResult{Status: ProbePresent} }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, chain, nil, nil, nil, alwaysReachable, commitsPresent, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.True(t, chained, "a handoff whose commits are present must chain its review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "a legitimate handoff must post no failure marker")
}

// A commit-presence check that could not be PERFORMED (git error, timeout,
// unresolvable project dir) must never be read as evidence the commits are
// missing — the live chain must not red the card, and must surface which probe
// failed and why so a human or a later daemon can act (SC-2403).
func TestHandleBoardAgentExit_UnreadableCommitCheckDoesNotFail(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }
	commitsPresent := func(string, []string) ProbeResult {
		return ProbeResult{Status: ProbeUnreadable, Detail: "probe timed out"}
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, chain, nil, nil, nil, alwaysReachable, commitsPresent, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "an unreadable check must not chain a review")
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], HandoffCheckUnreadableHeader),
		"expected a handoff-check-unreadable diagnostic, got %q", c.added[0])
	assert.False(t, strings.HasPrefix(c.added[0], ImplementationFailedHeader),
		"an unreadable check must not post the loud implementation-failed marker")
	assert.Contains(t, c.added[0], "probe timed out")
}

// The same distinction applies to the branch-reachability probe: an unreadable
// reachability check must not be treated as "branch absent" and must surface
// its reason instead of silently leaving the card with no explanation.
func TestHandleBoardAgentExit_UnreadableBranchCheckDoesNotFail(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }
	reachable := func(string) ProbeResult {
		return ProbeResult{Status: ProbeUnreadable, Detail: "project dir unresolved"}
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, chain, nil, nil, nil, reachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "an unreadable branch check must not chain a review")
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], HandoffCheckUnreadableHeader),
		"expected a handoff-check-unreadable diagnostic, got %q", c.added[0])
	assert.False(t, strings.HasPrefix(c.added[0], ImplementationFailedHeader),
		"an unreadable check must not post the loud implementation-failed marker")
	assert.Contains(t, c.added[0], "project dir unresolved")
}

// SC-782 merged verification stage: the autofix implementation container now
// runs the review in-place. When it already posted a [human:review-complete]
// (pass verdict) marker, the daemon must NOT launch a second cold review
// container — that recorded outcome already accounts for the review.
func TestHandleBoardAgentExit_InContainerReviewCompleteDoesNotChain(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewCompleteHeader+"\nverdict: pass", time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "an in-container review-complete must not chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "a completed in-container review must post no new marker")
}

// SC-782: an in-container review that completed with a FAIL verdict is the
// rework signal, already recorded. The daemon must not chain a second review
// and must not post any new marker.
func TestHandleBoardAgentExit_InContainerReviewFailedDoesNotChain(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewCompleteHeader+"\nverdict: fail", time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "an in-container review-complete (fail verdict) must not chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "the fail verdict is the recorded rework signal — post no new marker")
}

// SC-3156: the implementation agent is reaped (StopFailure) after posting its
// handoff, while a SEPARATE chained reviewer (board-<key>-verification) is alive
// and mid-review. The reap must NOT red the card: a stage is judged dead only on
// evidence about that stage, and the live verification agent owns its own
// liveness. No review-failed marker, no second chained review.
func TestHandleBoardAgentExit_ChainedReviewAliveNoReviewFailed(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewStartedHeader, time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }
	live := liveAgents(agentNameFor("SC-1", BoardVerification)) // the chained reviewer is alive

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "StopFailure", commenterFor, chain, live, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "a live chained review must not be re-chained")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "a live chained review must not draw a review-failed marker, got %v", c.added)
}

// SC-782: the implementation container died AFTER [human:review-started] but
// before the review completed, with NO separate verification agent alive — the
// merged SC-782 container was the reviewer. The daemon must surface a retryable
// [human:review-failed] instead of leaving the card spinning on a verification
// stage no agent owns — and must not chain a second cold review container.
func TestHandleBoardAgentExit_MergedReviewCrashPostsReviewFailed(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewStartedHeader, time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }
	live := liveAgents() // no separate verification agent alive

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "StopFailure", commenterFor, chain, live, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "a mid-review crash must not chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], ReviewFailedHeader),
		"a mid-review crash must post a retryable review-failed marker, got %q", c.added[0])
}

// SC-1688: a mid-review crash (no separate verification agent alive — the
// merged case) must put the diagnosed reason into the review-failed marker
// instead of the generic "retry the review" text. Pre-fix
// chainReviewAfterCleanBuild ignored the diagnoser and posted a hardcoded body.
func TestHandleBoardAgentExit_MergedReviewCrash_PostsDiagnosedReason(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewStartedHeader, time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	chain := func(string) error { return nil }
	live := liveAgents() // no separate verification agent alive
	diagnose := func(agentName, errorType string) FailureDiagnosis {
		return FailureDiagnosis{Headline: "command not found: gh", Detail: "exit code: 127"}
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "StopFailure", commenterFor, chain, live, nil, nil, alwaysReachable, nil, diagnose, nil, StageRetry{}, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], ReviewFailedHeader),
		"must still be a review-failed marker, got %q", c.added[0])
	assert.Contains(t, c.added[0], "command not found: gh",
		"marker must carry the diagnosed reason, got %q", c.added[0])
	assert.NotContains(t, c.added[0], "exited before completing the in-container review",
		"marker must not fall back to the hardcoded generic body")
	// AC #3 (SC-2133): when a handoff really is absent, the failure states what
	// was searched for, so the mismatch is diagnosable without reading agent logs.
	assert.Contains(t, c.added[0], ReviewCompleteHeader,
		"marker must name what it searched for, got %q", c.added[0])
}

// SC-2133: a merged implementation+review container posts BOTH
// [human:ready-for-review] and [human:review-started], then a moment later
// [human:review-complete] (pass), and exits cleanly (SessionEnd). The tracker
// read that fires on the exit event can race the second handoff exactly like
// SC-1484 raced the first — the settle-wait must keep re-reading while
// verification looks in-flight so the completed review is seen, and even if it
// is not seen in time, a clean exit must never be misread as a mid-review
// death. Neither a [human:review-failed] marker nor a second chained review
// may result.
func TestRunBoardFailureWatch_CleanExitLateReviewCompleteNoReviewFailed(t *testing.T) {
	origStep, origTries := boardExitRecheckStep, boardExitRecheckTries
	boardExitRecheckStep = 10 * time.Millisecond
	boardExitRecheckTries = 3
	t.Cleanup(func() {
		boardExitRecheckStep, boardExitRecheckTries = origStep, origTries
	})

	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &raceCommenter{
			snapshots: [][]tracker.Comment{
				{
					cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
					cmt(ReviewStartedHeader, time.Unix(2, 0)),
				},
				{
					cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
					cmt(ReviewStartedHeader, time.Unix(2, 0)),
					cmt(ReviewCompleteHeader+"\nverdict: pass", time.Unix(3, 0)),
				},
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		// A clean exit-0, not a reap: the container ran the review in place and
		// finished successfully.
		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			t.Fatalf("a clean exit whose review-complete merely lagged the read must not post any marker, got: %q", body)
		case <-chained:
			t.Fatal("a clean exit whose review already completed must not chain a second review")
		case <-time.After(500 * time.Millisecond):
		}

		c.mu.Lock()
		defer c.mu.Unlock()
		assert.Empty(t, c.added, "no failed marker for a clean exit racing its own late-propagating review-complete")
	})
}

// SC-2133: a clean exit (SessionEnd) whose review NEVER completes — not a race,
// genuinely still open — must still never be recorded as review-failed. Only a
// non-clean exit (StopFailure) may mean the review died mid-flight; a clean
// exit-0 racing a review-started marker means propagation hasn't caught up,
// never that the review died. The settle-wait's retry budget
// (boardExitRecheckTries) runs out against a comment thread that never changes.
func TestHandleBoardAgentExit_CleanExitReviewNeverCompletesNoReviewFailed(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewStartedHeader, time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "SessionEnd", commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	assert.False(t, chained, "a clean exit must never chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "a clean exit is never a mid-review death — no review-failed for a review that has not (yet) completed")
}

func TestRunBoardFailureWatch_ChainsReviewAfterCleanBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

		select {
		case pmKey := <-chained:
			assert.Equal(t, "SC-1", pmKey)
		case <-time.After(2 * time.Second):
			t.Fatal("expected the finished build to chain into a review")
		}
	})
}

// A board-context fix leaves its branch local on the machine that produced it.
// When the live exit fires on a daemon that cannot resolve that branch, the
// watcher must NOT chain a review it could never satisfy — it leaves the handoff
// for a daemon that can reach the branch (SC-652).
func TestRunBoardFailureWatch_SkipsChainWhenBranchUnreachable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: autofix/sc-1", time.Unix(1, 0))},
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }
		unreachable := func(string) ProbeResult { return ProbeResult{Status: ProbeAbsent} }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, unreachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

		select {
		case pmKey := <-chained:
			t.Fatalf("an unreachable handoff branch must not chain a review, got: %q", pmKey)
		case <-time.After(500 * time.Millisecond):
		}
	})
}

func TestRunBoardFailureWatch_NoChainForOtherStages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A cleanly finished PLANNING stage must not chain a review — only builds do.
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:plan-ready]", time.Unix(1, 0))},
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

		select {
		case <-chained:
			t.Fatal("planning completion must not chain a review")
		case <-time.After(300 * time.Millisecond):
		}
	})
}

// SC-206 contract pin: the zombie sweep reports a reap as a synthetic
// StopFailure event, so the watcher MUST keep accepting StopFailure and
// posting the stage's failed marker when only the started marker exists.
// Tightening the watcher's event filter would silently reopen the bug.
func TestRunBoardFailureWatch_SyntheticStopFailurePostsImplementationFailed(t *testing.T) {
	withInstantBoardExitRecheck(t)
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		// The reap-synthesized event carries no SessionID — only name + time.
		store.Append(hookevents.Event{EventName: "StopFailure", AgentName: "board-204-implementation", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			assert.True(t, strings.HasPrefix(body, ImplementationFailedHeader),
				"failed marker must lead the comment body, got: %q", body)
		case <-time.After(2 * time.Second):
			t.Fatal("expected a failed marker for the reaped implementation stage")
		}
	})
}

// ticket 405: an autofix run has a second legitimate ending — triage concludes
// not-a-bug, makes no code change, and stops with a terminal [human:no-fix-needed]
// marker and NO [human:ready-for-review] handoff. That is a clean stop: the
// watcher must post NO implementation-failed marker (no endless retry loop) and
// must NOT chain a review (there is no branch to review).
func TestRunBoardFailureWatch_NoFixNeededIsCleanStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{
				cmt(ImplementationStartedHeader, time.Unix(1, 0)),
				cmt(NoFixNeededHeader+"\nverdict: not-a-bug", time.Unix(2, 0)),
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			t.Fatalf("a not-a-bug clean stop must not post any marker, got: %q", body)
		case pmKey := <-chained:
			t.Fatalf("a not-a-bug clean stop must not chain a review, got: %q", pmKey)
		case <-time.After(500 * time.Millisecond):
		}

		c.mu.Lock()
		require.Empty(t, c.added)
		c.mu.Unlock()
	})
}

// ticket 405 (sibling verdict): undetermined also stops with no handoff and is
// misclassified identically. It posts the same terminal [human:no-fix-needed]
// marker and must be treated as a clean stop — no failed marker, no chain.
func TestRunBoardFailureWatch_UndeterminedIsCleanStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{
				cmt(ImplementationStartedHeader, time.Unix(1, 0)),
				cmt(NoFixNeededHeader+"\nverdict: undetermined", time.Unix(2, 0)),
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			t.Fatalf("an undetermined clean stop must not post any marker, got: %q", body)
		case pmKey := <-chained:
			t.Fatalf("an undetermined clean stop must not chain a review, got: %q", pmKey)
		case <-time.After(500 * time.Millisecond):
		}

		c.mu.Lock()
		require.Empty(t, c.added)
		c.mu.Unlock()
	})
}

// ticket 454 (planning twin of ticket 405): a planning run has a second
// legitimate ending — the planner verifies the ticket's work is already merged,
// refuses to attach a plan (a [human:plan-ready] plan would advance the card and
// re-implement shipped code), and stops with a terminal [human:nothing-to-do]
// marker and NO [human:plan-ready] handoff. That is a clean stop: the watcher
// must post NO planning-failed marker (no endless re-planning loop) and must NOT
// chain a review (there is no branch to review).
func TestRunBoardFailureWatch_NothingToDoIsCleanStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{
				cmt(PlanningStartedHeader, time.Unix(1, 0)),
				cmt(NothingToDoHeader+"\nevidence: already merged in PR #123", time.Unix(2, 0)),
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		chained := make(chan string, 1)
		chain := func(pmKey string) error { chained <- pmKey; return nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			t.Fatalf("a nothing-to-plan clean stop must not post any marker, got: %q", body)
		case pmKey := <-chained:
			t.Fatalf("a nothing-to-plan clean stop must not chain a review, got: %q", pmKey)
		case <-time.After(500 * time.Millisecond):
		}

		c.mu.Lock()
		require.Empty(t, c.added)
		c.mu.Unlock()
	})
}

// SC-751: planning has a third legitimate ending — the planner hit a genuine
// human fork, posted an up-front [human:options] block (stage: planning) and
// exited without a plan. That open same-stage options block is a clean pause,
// not a crash: the block stays open until the human picks (ApplyOption then
// relaunches planning with the choice injected). The watcher must post NO
// planning-failed marker, or the card would red and re-plan forever — the
// planning twin of the stranded-run class SC-731 fixed for worktrees.
func TestRunBoardFailureWatch_OpenPlanningOptionsIsCleanPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{
				cmt(PlanningStartedHeader, time.Unix(1, 0)),
				cmt("[human:options]\nstage: planning\ncontext: pick storage\n1: sqlite\n2: files", time.Unix(2, 0)),
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			t.Fatalf("an open same-stage options block is a clean pause — must post no failed marker, got: %q", body)
		case <-time.After(500 * time.Millisecond):
		}

		c.mu.Lock()
		require.Empty(t, c.added)
		c.mu.Unlock()
	})
}

// SC-751: the clean-pause guard is stage-precise. An open options block naming
// a DIFFERENT stage (implementation) does not belong to this planning run, so a
// planning agent that crashed while such a block is open must still surface a
// real planning-failed marker — the guard must not swallow unrelated crashes.
func TestRunBoardFailureWatch_OpenOptionsForOtherStageStillFails(t *testing.T) {
	withInstantBoardExitRecheck(t)
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{
				cmt(PlanningStartedHeader, time.Unix(1, 0)),
				cmt("[human:options]\nstage: implementation\ncontext: x\n1: a\n2: b", time.Unix(2, 0)),
			},
			addCh: make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

		select {
		case body := <-c.addCh:
			assert.Contains(t, body, PlanningFailedHeader)
		case <-time.After(2 * time.Second):
			t.Fatal("an options block for another stage must not suppress a real planning crash")
		}
	})
}

// SC-620: the failed marker's body is headline-first — the card badge/tooltip
// reads exactly the first non-header line via failureReason — followed by the
// diagnosis detail block for the detail pane.
func TestHandleBoardAgentExit_UsesDiagnoserHeadlineAndDetail(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	diag := func(agentName, errorType string) FailureDiagnosis {
		assert.Equal(t, "board-SC-1-implementation", agentName)
		return FailureDiagnosis{
			Headline: "claude exited with code 1: API Error",
			Detail:   "agent: board-SC-1-implementation\n\nlast output:\n~~~\nboom\n~~~",
		}
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, diag, nil, StageRetry{}, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	body := c.added[0]
	want := ImplementationFailedHeader + "\nclaude exited with code 1: API Error\n\nagent: board-SC-1-implementation\n\nlast output:\n~~~\nboom\n~~~"
	assert.Equal(t, want, body)
	// Contract pin: the card's one-line error is exactly the headline.
	assert.Equal(t, "claude exited with code 1: API Error", failureReason(body))
}

func TestHandleBoardAgentExit_NilDiagnoserFallsBackToGeneric(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningFailedHeader+"\n"+genericStageFailure, c.added[0])
}

// SC-2555 step 5b: a recorded failing model-call class is appended to the failed
// marker's detail so the card can state WHY a run failed (an unclassified "other"
// class here — SC-3024 now routes auth/rate-limit/overload/network/spend-limit to
// their own uncharged paused/needs-person paths before this enrichment is ever
// reached), while the headline the badge reads (failureReason) stays exactly the
// diagnoser's.
func TestHandleBoardAgentExit_AppendsModelOutcomeClass(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	diag := func(string, string) FailureDiagnosis {
		return FailureDiagnosis{Headline: "claude exited with code 1", Detail: "detail block"}
	}
	latest := func(ticket, stage string) (string, bool) {
		assert.Equal(t, "SC-1", ticket)
		assert.Equal(t, string(BoardImplementation), stage)
		return proxy.ClassOther, true
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, diag, nil, StageRetry{}, latest, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	body := c.added[0]
	assert.Contains(t, body, "\"other\"", "the marker must name the recorded outcome class")
	assert.Contains(t, body, "detail block", "the diagnoser detail is preserved")
	// The badge's one-line error is unchanged — the note lives only in the detail.
	assert.Equal(t, "claude exited with code 1", failureReason(body))
}

// A healthy last call (ClassOK) or an absent record must leave the marker
// byte-for-byte as it was without the seam — the enrichment is strictly additive.
func TestHandleBoardAgentExit_ModelOutcomeAdditiveWhenOKOrAbsent(t *testing.T) {
	withInstantBoardExitRecheck(t)
	cases := []struct {
		name   string
		latest LatestOutcomeClass
	}{
		{"nil lookup", nil},
		{"no record", func(string, string) (string, bool) { return "", false }},
		{"healthy last call", func(string, string) (string, bool) { return proxy.ClassOK, true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &syncCommenter{
				comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
			}
			commenterFor := func() (tracker.Commenter, error) { return c, nil }

			handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, tc.latest, "", zerolog.Nop())

			c.mu.Lock()
			defer c.mu.Unlock()
			require.Len(t, c.added, 1)
			assert.Equal(t, PlanningFailedHeader+"\n"+genericStageFailure, c.added[0],
				"the marker must be byte-for-byte unchanged when no failing outcome is recorded")
		})
	}
}

func TestHandleBoardAgentExit_EmptyHeadlineFallsBackToGeneric(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	diag := func(string, string) FailureDiagnosis { return FailureDiagnosis{} }

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, diag, nil, StageRetry{}, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningFailedHeader+"\n"+genericStageFailure, c.added[0])
}

// The watch loop must hand the hook event's error type to the diagnoser —
// a rate-limit stop is diagnosed from the event, not the artifacts.
func TestRunBoardFailureWatch_PassesErrorTypeToDiagnoser(t *testing.T) {
	withInstantBoardExitRecheck(t)
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }
		gotErrorType := make(chan string, 1)
		diag := func(_, errorType string) FailureDiagnosis {
			gotErrorType <- errorType
			return FailureDiagnosis{Headline: "Claude hit a rate limit and stopped"}
		}

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, diag, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "StopFailure", AgentName: "board-SC-1-planning", ErrorType: "rate_limit", Timestamp: time.Now()})

		select {
		case et := <-gotErrorType:
			assert.Equal(t, "rate_limit", et)
		case <-time.After(2 * time.Second):
			t.Fatal("diagnoser never received the event's error type")
		}
		select {
		case body := <-c.addCh:
			// SC-3024: a rate_limit hook errorType is now recognised as an
			// unavailability signal BEFORE the generic diagnose+failed path, so
			// it posts the paused *-outage marker instead of a red *-failed one
			// carrying the diagnoser's headline verbatim.
			assert.Contains(t, body, "paused")
			assert.Contains(t, body, "model usage limit")
		case <-time.After(2 * time.Second):
			t.Fatal("expected the paused outage marker")
		}
	})
}

func TestRunBoardFailureWatch_IgnoresNonBoardAgents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{addCh: make(chan string, 4)}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "some-other-agent", Timestamp: time.Now()})
		select {
		case <-c.addCh:
			t.Fatal("must ignore non-board agents")
		case <-time.After(300 * time.Millisecond):
		}
	})
}

// SC-2302: the pre-planning ticket-review gate runs UNDER the planning agent
// but files its verdict as a [human:ticket-review] marker classified to the
// BACKLOG stage. A deliberate hard-stop verdict (rejected/superseded/escalated)
// is the gate correctly refusing to start work — a clean stop, not a crash. The
// watcher must post NO planning-failed marker, must NOT chain a review, and must
// reclaim the run's worktree (onHandoff fired) exactly like every other clean
// ending. Because the marker is classified to backlog, scoping the clean-stop
// check to the running (planning) stage misses it — the fix reads the verdict
// stage-agnostically via deliberateStopRecorded.
func assertTicketReviewVerdictIsCleanStop(t *testing.T, verdict string) {
	t.Helper()
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt(PlanningStartedHeader, time.Unix(1, 0)),
			cmt(TicketReviewedHeader+" "+verdict, time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained []string
	chain := func(pmKey string) error { chained = append(chained, pmKey); return nil }
	var reclaimed string
	onHandoff := func(agentName string) { reclaimed = agentName }

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "crashed", "", commenterFor, chain, nil, nil, nil, alwaysReachable, nil, nil, onHandoff, StageRetry{}, nil, "", zerolog.Nop())

	c.mu.Lock()
	assert.Empty(t, c.added, "a deliberate %s stop must post no failed marker", verdict)
	c.mu.Unlock()
	assert.Empty(t, chained, "a deliberate %s stop must not chain a review", verdict)
	assert.Equal(t, "board-SC-1-planning", reclaimed, "a deliberate %s stop must reclaim the worktree", verdict)
}

func TestRunBoardFailureWatch_TicketReviewRejectedIsCleanStop(t *testing.T) {
	assertTicketReviewVerdictIsCleanStop(t, "rejected")
}

func TestRunBoardFailureWatch_TicketReviewSupersededIsCleanStop(t *testing.T) {
	assertTicketReviewVerdictIsCleanStop(t, "superseded")
}

func TestRunBoardFailureWatch_TicketReviewEscalatedIsCleanStop(t *testing.T) {
	assertTicketReviewVerdictIsCleanStop(t, "escalated")
}

// SC-2302 guard: the distinction follows the verdict, not the mere presence of a
// ticket-review marker. `ready` (and `reframed`) mean the ticket is fine and the
// work continues into planning. A planning agent that carries a `ready` verdict
// but dies before posting [human:plan-ready] is a genuine crash and MUST still
// post a planning-failed marker — proving deliberateStopRecorded does not swallow
// the non-stop verdicts.
func TestRunBoardFailureWatch_TicketReviewReadyThenCrashStillFails(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt(PlanningStartedHeader, time.Unix(1, 0)),
			cmt(TicketReviewedHeader+" ready", time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "crashed", "", commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1, "a ready verdict that then crashes must still surface a real failure")
	assert.Contains(t, c.added[0], PlanningFailedHeader)
}

// SC-2447: a silence-reap (the zombie sweep reaping a board agent that went
// quiet past its idle budget with no sign of life) is a machine-chosen stop,
// not a stage failure — it must not consume the ticket's automatic-retry
// budget. The sentinel ErrorType carries the observed idle so the marker can
// say plainly what was observed and why.
func TestHandleBoardAgentExit_SilenceReapDoesNotChargeRetry(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var attemptsCalled bool
	var relaunched []BoardStage
	retry := StageRetry{
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { attemptsCalled = true; return 1, nil },
		Relaunch: func(_ string, s BoardStage) error { relaunched = append(relaunched, s); return nil },
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", ReapSilenceErrorType+":18m0s", "StopFailure",
		commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, retry, nil, "", zerolog.Nop())

	assert.False(t, attemptsCalled, "a silence reap must never read/bump the charged attempt counter")
	assert.Equal(t, []BoardStage{BoardImplementation}, relaunched, "the stage is still relaunched")

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], ImplementationFailedHeader))
	assert.Contains(t, c.added[0], "18m", "the marker must state what was observed")
	assert.Contains(t, c.added[0], "not charged", "the marker must state the rule applied")
}

// A genuine StopFailure — no silence sentinel — must still charge the retry
// budget exactly as before; only a recognized silence-reap sentinel is exempt.
func TestHandleBoardAgentExit_GenuineStopFailureStillCharges(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var attemptsCalled bool
	retry := StageRetry{
		Outcome:  func(string, BoardStage) (string, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { attemptsCalled = true; return 1, nil },
		Relaunch: func(string, BoardStage) error { return nil },
	}

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "StopFailure",
		commenterFor, nil, nil, nil, nil, alwaysReachable, nil, nil, nil, retry, nil, "", zerolog.Nop())

	assert.True(t, attemptsCalled, "an empty-ErrorType StopFailure must still charge the retry budget")
}

// A deploy-fixer's Stop event routes to AdvanceDeployFix (reclaiming its
// worktree first) and is fully handled there — never falling through to the
// generic stage-failure diagnoser that would red the card.
func TestHandleBoardAgentExit_DeployFixStage_RoutesToAdvance(t *testing.T) {
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var advanced []string
	advanceDeployFix := func(pmKey string) error { advanced = append(advanced, pmKey); return nil }
	var reclaimed string
	onHandoff := func(agentName string) { reclaimed = agentName }

	handleBoardAgentExit(context.Background(), "board-SC-1-deployfix", "", "", commenterFor, nil, nil, nil, advanceDeployFix, alwaysReachable, nil, nil, onHandoff, StageRetry{}, nil, "", zerolog.Nop())

	assert.Equal(t, []string{"SC-1"}, advanced, "the deploy-fix driver must be invoked once")
	assert.Equal(t, "board-SC-1-deployfix", reclaimed, "the fixer's worktree must be reclaimed")
	assert.Empty(t, c.added, "a deploy-fix exit must not post a generic stage-failed marker")
}
