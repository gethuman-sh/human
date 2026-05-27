package daemon

import (
	"context"
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
