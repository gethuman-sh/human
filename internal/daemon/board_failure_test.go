package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
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
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))},
		addCh:    make(chan string, 4),
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, nil, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

	select {
	case body := <-c.addCh:
		assert.Contains(t, body, PlanningFailedHeader)
	case <-time.After(2 * time.Second):
		t.Fatal("expected a failed marker to be posted")
	}
}

// SC-201: board stage agents reuse the same deterministic name on every
// rebuild (agentNameFor is deterministic; the rework path, forward
// Implementation and ApplyFix all re-launch the same name). The watcher must
// handle EVERY exit of a reused name, not just the first — a name-keyed
// lifetime dedupe silently dropped second-and-later runs.
func TestRunBoardFailureWatch_ReusedNameSecondIncompleteExitPostsAgain(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(PlanningStartedHeader, time.Unix(1, 0))},
		addCh:    make(chan string, 4),
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, nil, zerolog.Nop())
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
}

// SC-201: a second cleanly-finished build of the same reused name must chain
// into review again, not be swallowed by lifetime dedupe.
func TestRunBoardFailureWatch_ReusedNameSecondCleanBuildChainsAgain(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	chained := make(chan string, 2)
	chain := func(pmKey string) error { chained <- pmKey; return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, chain, zerolog.Nop())
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
}

func TestRunBoardFailureWatch_NoPostWhenStageDone(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0))},
		addCh:    make(chan string, 4),
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, nil, zerolog.Nop())
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
}

func TestHandleBoardAgentExit_MalformedName(t *testing.T) {
	var called bool
	commenterFor := func() (tracker.Commenter, error) {
		called = true
		return &syncCommenter{}, nil
	}
	// A name that does not parse must short-circuit before resolving a commenter.
	handleBoardAgentExit(context.Background(), "board-", commenterFor, nil, zerolog.Nop())
	assert.False(t, called)
}

func TestHandleBoardAgentExit_CommenterError(t *testing.T) {
	commenterFor := func() (tracker.Commenter, error) {
		return nil, assertErr{}
	}
	// Must not panic when the commenter cannot be resolved.
	handleBoardAgentExit(context.Background(), "board-SC-1-planning", commenterFor, nil, zerolog.Nop())
}

type assertErr struct{}

func (assertErr) Error() string { return "no commenter" }

func TestRunBoardFailureWatch_ChainsReviewAfterCleanBuild(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	chained := make(chan string, 1)
	chain := func(pmKey string) error { chained <- pmKey; return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, chain, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-implementation", Timestamp: time.Now()})

	select {
	case pmKey := <-chained:
		assert.Equal(t, "SC-1", pmKey)
	case <-time.After(2 * time.Second):
		t.Fatal("expected the finished build to chain into a review")
	}
}

func TestRunBoardFailureWatch_NoChainForOtherStages(t *testing.T) {
	// A cleanly finished PLANNING stage must not chain a review — only builds do.
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt("[human:plan-ready]", time.Unix(1, 0))},
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	chained := make(chan string, 1)
	chain := func(pmKey string) error { chained <- pmKey; return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, chain, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

	select {
	case <-chained:
		t.Fatal("planning completion must not chain a review")
	case <-time.After(300 * time.Millisecond):
	}
}

// SC-206 contract pin: the zombie sweep reports a reap as a synthetic
// StopFailure event, so the watcher MUST keep accepting StopFailure and
// posting the stage's failed marker when only the started marker exists.
// Tightening the watcher's event filter would silently reopen the bug.
func TestRunBoardFailureWatch_SyntheticStopFailurePostsImplementationFailed(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{
		comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))},
		addCh:    make(chan string, 4),
	}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, nil, zerolog.Nop())
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
}

// ticket 405: an autofix run has a second legitimate ending — triage concludes
// not-a-bug, makes no code change, and stops with a terminal [human:no-fix-needed]
// marker and NO [human:ready-for-review] handoff. That is a clean stop: the
// watcher must post NO implementation-failed marker (no endless retry loop) and
// must NOT chain a review (there is no branch to review).
func TestRunBoardFailureWatch_NoFixNeededIsCleanStop(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, chain, zerolog.Nop())
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
}

// ticket 405 (sibling verdict): undetermined also stops with no handoff and is
// misclassified identically. It posts the same terminal [human:no-fix-needed]
// marker and must be treated as a clean stop — no failed marker, no chain.
func TestRunBoardFailureWatch_UndeterminedIsCleanStop(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, chain, zerolog.Nop())
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
}

// ticket 454 (planning twin of ticket 405): a planning run has a second
// legitimate ending — the planner verifies the ticket's work is already merged,
// refuses to attach a plan (a [human:plan-ready] plan would advance the card and
// re-implement shipped code), and stops with a terminal [human:nothing-to-do]
// marker and NO [human:plan-ready] handoff. That is a clean stop: the watcher
// must post NO planning-failed marker (no endless re-planning loop) and must NOT
// chain a review (there is no branch to review).
func TestRunBoardFailureWatch_NothingToDoIsCleanStop(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, chain, zerolog.Nop())
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
}

func TestRunBoardFailureWatch_IgnoresNonBoardAgents(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{addCh: make(chan string, 4)}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, nil, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "some-other-agent", Timestamp: time.Now()})
	select {
	case <-c.addCh:
		t.Fatal("must ignore non-board agents")
	case <-time.After(300 * time.Millisecond):
	}
}
