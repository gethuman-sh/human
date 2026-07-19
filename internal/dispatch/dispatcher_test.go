package dispatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type mockSource struct {
	mu       sync.Mutex
	messages []QueuedMessage
	acked    []int
	fetchErr error
	ackErr   error
}

func (m *mockSource) FetchMessages(_ context.Context) ([]QueuedMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	return m.messages, nil
}

func (m *mockSource) AckMessage(_ context.Context, updateID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ackErr != nil {
		return m.ackErr
	}
	m.acked = append(m.acked, updateID)
	return nil
}

type mockFinder struct {
	agents  []Agent
	findErr error
}

func (m *mockFinder) FindIdleAgents(_ context.Context) ([]Agent, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.agents, nil
}

type sendCall struct {
	Agent  Agent
	Prompt string
}

type mockSender struct {
	mu      sync.Mutex
	calls   []sendCall
	sendErr error
}

func (m *mockSender) SendPrompt(_ context.Context, agent Agent, prompt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	m.calls = append(m.calls, sendCall{Agent: agent, Prompt: prompt})
	return nil
}

type notifyCall struct {
	ChatID int64
	Text   string
}

type mockNotifier struct {
	mu        sync.Mutex
	calls     []notifyCall
	notifyErr error
}

func (m *mockNotifier) Notify(_ context.Context, chatID int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.notifyErr != nil {
		return m.notifyErr
	}
	m.calls = append(m.calls, notifyCall{ChatID: chatID, Text: text})
	return nil
}

func newTestDispatcher(source *mockSource, finder *mockFinder, sender *mockSender, notifier *mockNotifier) *Dispatcher {
	return &Dispatcher{
		Source:   source,
		Finder:   finder,
		Sender:   sender,
		Notifier: notifier,
		Config:   Config{PollInterval: 50 * time.Millisecond},
		Logger:   zerolog.Nop(),
	}
}

// --- Tests ---

func TestDispatcher_DispatchesMessageToIdleAgent(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "fix the login bug"},
		},
	}
	agent := Agent{SessionName: "claude", WindowIndex: 0, PaneIndex: 1, Label: "claude:0.1"}
	finder := &mockFinder{agents: []Agent{agent}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	ctx, cancel := context.WithCancel(context.Background())

	// Run one tick manually.
	d.seen = make(map[int]bool)
	d.tick(ctx)
	cancel()

	require.Len(t, sender.calls, 1)
	assert.Contains(t, sender.calls[0].Prompt, "fix the login bug")
	assert.Equal(t, agent, sender.calls[0].Agent)

	require.Len(t, source.acked, 1)
	assert.Equal(t, 100, source.acked[0])

	require.Len(t, notifier.calls, 1)
	assert.Equal(t, int64(42), notifier.calls[0].ChatID)
	assert.Contains(t, notifier.calls[0].Text, "claude:0.1")
}

func TestDispatcher_FIFO(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "first"},
			{UpdateID: 101, ChatID: 42, From: "John", Text: "second"},
			{UpdateID: 102, ChatID: 42, From: "John", Text: "third"},
		},
	}
	finder := &mockFinder{agents: []Agent{
		{SessionName: "claude", WindowIndex: 0, PaneIndex: 0, Label: "claude:0.0"},
	}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	// Only one agent available, so only the first message dispatched.
	require.Len(t, sender.calls, 1)
	assert.Contains(t, sender.calls[0].Prompt, "first")
	assert.Len(t, d.queue, 2)
}

func TestDispatcher_MultipleAgents(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "first"},
			{UpdateID: 101, ChatID: 42, From: "John", Text: "second"},
		},
	}
	finder := &mockFinder{agents: []Agent{
		{SessionName: "claude", WindowIndex: 0, PaneIndex: 0, Label: "claude:0.0"},
		{SessionName: "claude", WindowIndex: 0, PaneIndex: 1, Label: "claude:0.1"},
	}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	require.Len(t, sender.calls, 2)
	assert.Contains(t, sender.calls[0].Prompt, "first")
	assert.Contains(t, sender.calls[1].Prompt, "second")
	assert.Empty(t, d.queue)
}

func TestDispatcher_NoIdleAgents(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "task"},
		},
	}
	finder := &mockFinder{agents: nil}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	assert.Empty(t, sender.calls)
	assert.Len(t, d.queue, 1, "message should remain queued")
}

func TestDispatcher_NoMessages(t *testing.T) {
	source := &mockSource{messages: nil}
	finder := &mockFinder{agents: []Agent{{Label: "claude:0.0"}}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	assert.Empty(t, sender.calls)
}

func TestDispatcher_FetchError(t *testing.T) {
	source := &mockSource{fetchErr: fmt.Errorf("network error")}
	finder := &mockFinder{}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	// Pre-queue a message to verify it survives the error.
	d.queue = []QueuedMessage{{UpdateID: 99, ChatID: 1, Text: "existing"}}
	d.seen[99] = true
	d.tick(context.Background())

	assert.Len(t, d.queue, 1, "existing queue should be preserved")
}

func TestDispatcher_SendError(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "task"},
		},
	}
	finder := &mockFinder{agents: []Agent{{Label: "claude:0.0"}}}
	sender := &mockSender{sendErr: fmt.Errorf("tmux error")}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	assert.Len(t, d.queue, 1, "message should remain queued on send error")
	assert.Empty(t, source.acked, "should not ack on send failure")
	assert.Empty(t, notifier.calls, "should not notify on send failure")
}

func TestDispatcher_NotifyError(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "task"},
		},
	}
	finder := &mockFinder{agents: []Agent{{Label: "claude:0.0"}}}
	sender := &mockSender{}
	notifier := &mockNotifier{notifyErr: fmt.Errorf("telegram error")}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	// Dispatch should succeed even if notification fails.
	require.Len(t, sender.calls, 1)
	require.Len(t, source.acked, 1)
	assert.Empty(t, d.queue)
}

func TestDispatcher_Deduplication(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "task"},
		},
	}
	finder := &mockFinder{agents: []Agent{{Label: "claude:0.0"}}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)

	// First tick: dispatches the message.
	d.tick(context.Background())
	require.Len(t, sender.calls, 1)

	// Second tick: same message returned by Telegram (not yet acked on server).
	d.tick(context.Background())
	assert.Len(t, sender.calls, 1, "should not dispatch same message twice")
}

func TestDispatcher_ContextCancellation(t *testing.T) {
	source := &mockSource{}
	finder := &mockFinder{}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx)
	}()

	cancel()
	err := <-done
	assert.NoError(t, err)
}

func TestBuildPrompt(t *testing.T) {
	prompt := buildPrompt("  fix the login bug  ")
	assert.Contains(t, prompt, "fix the login bug")
	assert.NotContains(t, prompt, "  fix") // trimmed
}

func TestBuildPrompt_StripsControlChars(t *testing.T) {
	prompt := buildPrompt("fix \x00the\x1bbug\x07")
	assert.Contains(t, prompt, "fix thebug")
	assert.NotContains(t, prompt, "\x00")
	assert.NotContains(t, prompt, "\x1b")
	assert.NotContains(t, prompt, "\x07")
}

// Oversized messages are truncated to the byte cap with a marker, so a
// compromised allowlisted account cannot flood a Claude agent with a
// multi-KB jailbreak. The marker lets Claude see that text was cut.
func TestBuildPrompt_TruncatesOversizedInput(t *testing.T) {
	input := strings.Repeat("x", 5000)
	prompt := buildPrompt(input)

	assert.Contains(t, prompt, "[truncated]")

	// The prompt has a prefix from DefaultPromptTemplate. The message
	// portion (what follows the prefix) must be <= maxMessageTextLen plus
	// the " [truncated]" marker, not 5000.
	prefix := fmt.Sprintf(DefaultPromptTemplate, "")
	messagePart := strings.TrimPrefix(prompt, prefix)
	assert.LessOrEqual(t, len(messagePart), maxMessageTextLen+len(" [truncated]"))
	assert.Greater(t, len(messagePart), maxMessageTextLen) // truncation actually happened
}

// A message exactly at the cap is left untouched — no off-by-one.
func TestBuildPrompt_AtCapNotTruncated(t *testing.T) {
	input := strings.Repeat("y", maxMessageTextLen)
	prompt := buildPrompt(input)
	assert.NotContains(t, prompt, "[truncated]")
	assert.Contains(t, prompt, input)
}

func TestDispatcher_FinderError(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "task"},
		},
	}
	finder := &mockFinder{findErr: fmt.Errorf("tmux not found")}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	assert.Empty(t, sender.calls)
	assert.Len(t, d.queue, 1, "message should remain queued")
}

func TestDispatcher_AckError(t *testing.T) {
	source := &mockSource{
		messages: []QueuedMessage{
			{UpdateID: 100, ChatID: 42, From: "John", Text: "task"},
		},
		ackErr: fmt.Errorf("ack failed"),
	}
	finder := &mockFinder{agents: []Agent{{Label: "claude:0.0"}}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)
	d.tick(context.Background())

	// Message should still be dispatched and dequeued even if ack fails.
	require.Len(t, sender.calls, 1)
	assert.Empty(t, d.queue)
}

func TestDispatcher_SeenMapPruning(t *testing.T) {
	// Fill the seen map beyond maxSeenSize to trigger pruning.
	source := &mockSource{}
	finder := &mockFinder{agents: []Agent{{Label: "claude:0.0"}}}
	sender := &mockSender{}
	notifier := &mockNotifier{}

	d := newTestDispatcher(source, finder, sender, notifier)
	d.seen = make(map[int]bool)

	// Pre-fill seen with maxSeenSize+100 entries.
	for i := range maxSeenSize + 100 {
		d.seen[i] = true
	}

	// Add one queued message so dispatchMessages runs and triggers pruneSeen.
	d.queue = []QueuedMessage{{UpdateID: maxSeenSize + 200, ChatID: 42, Text: "task"}}
	d.seen[maxSeenSize+200] = true

	d.dispatchMessages(context.Background())

	require.Len(t, sender.calls, 1)
	// After pruning, seen should be bounded to maxSeenSize/2.
	assert.LessOrEqual(t, len(d.seen), maxSeenSize, "seen map should be pruned")
	// The most recent IDs should still be present.
	assert.True(t, d.seen[maxSeenSize+99], "recent IDs should survive pruning")
	// The oldest IDs should be evicted.
	assert.False(t, d.seen[0], "oldest IDs should be pruned")
}
