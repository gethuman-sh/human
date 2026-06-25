package daemon

import (
	"context"
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
	go RunBoardFailureWatch(ctx, store, commenterFor, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})

	select {
	case body := <-c.addCh:
		assert.Contains(t, body, PlanningFailedHeader)
	case <-time.After(2 * time.Second):
		t.Fatal("expected a failed marker to be posted")
	}

	// A repeat event for the same agent must not post twice.
	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-SC-1-planning", Timestamp: time.Now()})
	select {
	case <-c.addCh:
		t.Fatal("must not post failed marker twice for the same agent")
	case <-time.After(300 * time.Millisecond):
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
	go RunBoardFailureWatch(ctx, store, commenterFor, zerolog.Nop())
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
	handleBoardAgentExit(context.Background(), "board-", commenterFor, zerolog.Nop())
	assert.False(t, called)
}

func TestHandleBoardAgentExit_CommenterError(t *testing.T) {
	commenterFor := func() (tracker.Commenter, error) {
		return nil, assertErr{}
	}
	// Must not panic when the commenter cannot be resolved.
	handleBoardAgentExit(context.Background(), "board-SC-1-planning", commenterFor, zerolog.Nop())
}

type assertErr struct{}

func (assertErr) Error() string { return "no commenter" }

func TestRunBoardFailureWatch_IgnoresNonBoardAgents(t *testing.T) {
	store := NewHookEventStore()
	c := &syncCommenter{addCh: make(chan string, 4)}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunBoardFailureWatch(ctx, store, commenterFor, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "some-other-agent", Timestamp: time.Now()})
	select {
	case <-c.addCh:
		t.Fatal("must ignore non-board agents")
	case <-time.After(300 * time.Millisecond):
	}
}
