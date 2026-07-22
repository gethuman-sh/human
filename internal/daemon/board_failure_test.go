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

func TestRunBoardFailureWatch_PostsFailedOnIncompleteStage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
	handleBoardAgentExit(context.Background(), "board-", "", commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
	assert.False(t, called)
}

func TestHandleBoardAgentExit_CommenterError(t *testing.T) {
	commenterFor := func() (tracker.Commenter, error) {
		return nil, assertErr{}
	}
	// Must not panic when the commenter cannot be resolved.
	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
	commitsPresent := func(string, []string) bool { return false }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", commenterFor, chain, alwaysReachable, commitsPresent, nil, nil, "", zerolog.Nop())

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
	commitsPresent := func(string, []string) bool { return true }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", commenterFor, chain, alwaysReachable, commitsPresent, nil, nil, "", zerolog.Nop())

	assert.True(t, chained, "a handoff whose commits are present must chain its review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "a legitimate handoff must post no failure marker")
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

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())

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

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())

	assert.False(t, chained, "an in-container review-complete (fail verdict) must not chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "the fail verdict is the recorded rework signal — post no new marker")
}

// SC-782: the implementation container died AFTER [human:review-started] but
// before the review completed. The daemon must surface a retryable
// [human:review-failed] instead of leaving the card spinning on a verification
// stage no agent owns — and must not chain a second cold review container.
func TestHandleBoardAgentExit_MidReviewCrashPostsReviewFailed(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{
			cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
			cmt(ReviewStartedHeader, time.Unix(2, 0)),
		},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var chained bool
	chain := func(string) error { chained = true; return nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())

	assert.False(t, chained, "a mid-review crash must not chain a second review")
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], ReviewFailedHeader),
		"a mid-review crash must post a retryable review-failed marker, got %q", c.added[0])
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
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		unreachable := func(string) bool { return false }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, unreachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{
			comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
			addCh:    make(chan string, 4),
		}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, chain, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
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

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", commenterFor, nil, alwaysReachable, nil, diag, nil, "", zerolog.Nop())

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
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningFailedHeader+"\n"+genericStageFailure, c.added[0])
}

func TestHandleBoardAgentExit_EmptyHeadlineFallsBackToGeneric(t *testing.T) {
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	diag := func(string, string) FailureDiagnosis { return FailureDiagnosis{} }

	handleBoardAgentExit(context.Background(), "board-SC-1-planning", "", commenterFor, nil, alwaysReachable, nil, diag, nil, "", zerolog.Nop())

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningFailedHeader+"\n"+genericStageFailure, c.added[0])
}

// The watch loop must hand the hook event's error type to the diagnoser —
// a rate-limit stop is diagnosed from the event, not the artifacts.
func TestRunBoardFailureWatch_PassesErrorTypeToDiagnoser(t *testing.T) {
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
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, diag, nil, "", zerolog.Nop())
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
			assert.Contains(t, body, "rate limit")
		case <-time.After(2 * time.Second):
			t.Fatal("expected the diagnosed failed marker")
		}
	})
}

func TestRunBoardFailureWatch_IgnoresNonBoardAgents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewHookEventStore()
		c := &syncCommenter{addCh: make(chan string, 4)}
		commenterFor := func() (tracker.Commenter, error) { return c, nil }

		ctx := t.Context()
		go RunBoardFailureWatch(ctx, store, commenterFor, nil, alwaysReachable, nil, nil, nil, "", zerolog.Nop())
		time.Sleep(50 * time.Millisecond)

		store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "some-other-agent", Timestamp: time.Now()})
		select {
		case <-c.addCh:
			t.Fatal("must ignore non-board agents")
		case <-time.After(300 * time.Millisecond):
		}
	})
}
