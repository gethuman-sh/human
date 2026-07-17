package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

type mockCleaner struct {
	deleted []string
}

func (m *mockCleaner) DeleteAgent(_ context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	return nil
}

func (m *mockCleaner) DecommissionAgent(name string) (string, error) {
	return "container-" + name, nil
}

func (m *mockCleaner) StopContainer(_ context.Context, _ string) error {
	return nil
}

// countingCleaner is concurrency-safe: the fix makes cleanup fire once per
// exit, so two exits of the same name run two goroutines that both record.
type countingCleaner struct {
	mu    sync.Mutex
	count map[string]int
	ch    chan string
}

func newCountingCleaner() *countingCleaner {
	return &countingCleaner{count: make(map[string]int), ch: make(chan string, 4)}
}

func (c *countingCleaner) DeleteAgent(_ context.Context, name string) error {
	c.mu.Lock()
	c.count[name]++
	c.mu.Unlock()
	c.ch <- name
	return nil
}

func (c *countingCleaner) DecommissionAgent(name string) (string, error) {
	return "container-" + name, nil
}

func (c *countingCleaner) StopContainer(_ context.Context, _ string) error { return nil }

// SC-201: the cleanup watcher deduped by name for the daemon's lifetime, so a
// re-run reusing the same board stage agent name never got its container and
// worktree cleaned up. Every exit must clean up.
func TestRunAgentCleanup_ReusedNameSecondExitCleansAgain(t *testing.T) {
	store := NewHookEventStore()
	cleaner := newCountingCleaner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunAgentCleanup(ctx, store, cleaner, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	name := "board-201-implementation"
	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: name, Timestamp: time.Now()})
	select {
	case got := <-cleaner.ch:
		assert.Equal(t, name, got)
	case <-time.After(4 * time.Second):
		t.Fatal("expected first cleanup")
	}

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: name, Timestamp: time.Now()})
	select {
	case got := <-cleaner.ch:
		assert.Equal(t, name, got)
	case <-time.After(4 * time.Second):
		t.Fatal("second exit of a reused agent name must be cleaned up again (SC-201)")
	}

	cleaner.mu.Lock()
	assert.Equal(t, 2, cleaner.count[name], "reused name must be cleaned once per exit")
	cleaner.mu.Unlock()
}

func TestRunAgentCleanup_StopEvent(t *testing.T) {
	store := NewHookEventStore()
	cleaner := &mockCleaner{}
	logger := zerolog.Nop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunAgentCleanup(ctx, store, cleaner, logger)
		close(done)
	}()

	// Let the goroutine subscribe before appending.
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{
		EventName: "Stop",
		SessionID: "s1",
		AgentName: "agent-1",
		Timestamp: time.Now(),
	})

	// Wait for cleanup goroutine to process (3s delay + margin).
	time.Sleep(4 * time.Second)
	cancel()
	<-done

	assert.Contains(t, cleaner.deleted, "agent-1")
}

func TestRunAgentCleanup_SessionEndEvent(t *testing.T) {
	store := NewHookEventStore()
	cleaner := &mockCleaner{}
	logger := zerolog.Nop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunAgentCleanup(ctx, store, cleaner, logger)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{
		EventName: "SessionEnd",
		SessionID: "s1",
		AgentName: "agent-2",
		Timestamp: time.Now(),
	})

	time.Sleep(4 * time.Second)
	cancel()
	<-done

	assert.Contains(t, cleaner.deleted, "agent-2")
}

func TestRunAgentCleanup_IgnoresNonAgentEvents(t *testing.T) {
	store := NewHookEventStore()
	cleaner := &mockCleaner{}
	logger := zerolog.Nop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunAgentCleanup(ctx, store, cleaner, logger)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Event without AgentName should be ignored.
	store.Append(hookevents.Event{
		EventName: "Stop",
		SessionID: "s1",
		Timestamp: time.Now(),
	})

	time.Sleep(4 * time.Second)
	cancel()
	<-done

	assert.Empty(t, cleaner.deleted)
}
