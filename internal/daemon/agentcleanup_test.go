package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

	ctx := t.Context()
	go RunAgentCleanup(ctx, store, cleaner, nil, zerolog.Nop())
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
		RunAgentCleanup(ctx, store, cleaner, nil, logger)
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
		RunAgentCleanup(ctx, store, cleaner, nil, logger)
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
		RunAgentCleanup(ctx, store, cleaner, nil, logger)
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

// shrinkCleanupWait makes the exit wait test-fast and restores it afterwards,
// so a regression test costs milliseconds rather than the production budget.
func shrinkCleanupWait(t *testing.T, wait time.Duration) {
	t.Helper()
	poll, w := cleanupExitPoll, cleanupExitWait
	cleanupExitPoll, cleanupExitWait = 5*time.Millisecond, wait
	t.Cleanup(func() { cleanupExitPoll, cleanupExitWait = poll, w })
}

// SC-3785: an exit hook event names the CONTAINER's agent, so a subagent's Stop
// is indistinguishable from its parent's. Tearing the container down on that
// event SIGKILLed runs that were still working. A run whose claude is provably
// still running must be left alone.
func TestRunAgentCleanup_LeavesRunAliveWhenClaudeStillRunning(t *testing.T) {
	shrinkCleanupWait(t, 50*time.Millisecond)
	store := NewHookEventStore()
	cleaner := &mockCleaner{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	alive := func(context.Context, string) (bool, error) { return true, nil }
	go RunAgentCleanup(ctx, store, cleaner, alive, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "Stop", AgentName: "board-3785-verification", Timestamp: time.Now()})

	time.Sleep(500 * time.Millisecond)
	assert.Empty(t, cleaner.deleted, "a run whose claude is still running must not be torn down by somebody else's Stop")
}

// The other half of the same contract: once claude really is gone, the teardown
// still happens — the wait defers cleanup, it does not cancel it.
func TestRunAgentCleanup_DeletesOnceClaudeExits(t *testing.T) {
	shrinkCleanupWait(t, 5*time.Second)
	store := NewHookEventStore()
	cleaner := newCountingCleaner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var probes int32
	alive := func(context.Context, string) (bool, error) {
		// Alive for the first two probes, gone after — the shape of a run that
		// takes a moment to unwind after firing its exit hook.
		return atomic.AddInt32(&probes, 1) <= 2, nil
	}
	go RunAgentCleanup(ctx, store, cleaner, alive, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "Stop", AgentName: "board-3785-implementation", Timestamp: time.Now()})

	select {
	case got := <-cleaner.ch:
		assert.Equal(t, "board-3785-implementation", got)
	case <-time.After(4 * time.Second):
		t.Fatal("teardown must still happen once claude has exited")
	}
}

// An agent that cannot be probed is not evidence of a live run — the container
// is unreachable — so teardown proceeds exactly as it did before the probe
// existed. Sparing it would strand containers whenever docker hiccups.
func TestRunAgentCleanup_ProbeErrorTearsDown(t *testing.T) {
	shrinkCleanupWait(t, 5*time.Second)
	store := NewHookEventStore()
	cleaner := newCountingCleaner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	alive := func(context.Context, string) (bool, error) {
		return false, errors.New("no such container")
	}
	go RunAgentCleanup(ctx, store, cleaner, alive, zerolog.Nop())
	time.Sleep(50 * time.Millisecond)

	store.Append(hookevents.Event{EventName: "SessionEnd", AgentName: "board-3785-planning", Timestamp: time.Now()})

	select {
	case got := <-cleaner.ch:
		assert.Equal(t, "board-3785-planning", got)
	case <-time.After(4 * time.Second):
		t.Fatal("an unreachable agent must still be cleaned up")
	}
}
