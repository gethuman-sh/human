package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/claude/logparser"
)

func TestHookEventStore_AppendAndSnapshot(t *testing.T) {
	store := NewHookEventStore()

	store.Append(hookevents.Event{
		EventName: "UserPromptSubmit",
		SessionID: "s1",
		Cwd:       "/proj",
		Timestamp: time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC),
	})
	store.Append(hookevents.Event{
		EventName: "Stop",
		SessionID: "s1",
		Cwd:       "/proj",
		Timestamp: time.Date(2026, 3, 25, 10, 0, 5, 0, time.UTC),
	})

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, logparser.StatusReady, snap["s1"].Status)
	assert.Equal(t, "/proj", snap["s1"].Cwd)
}

func TestHookEventStore_MultipleSessions(t *testing.T) {
	store := NewHookEventStore()

	store.Append(hookevents.Event{
		EventName: "UserPromptSubmit",
		SessionID: "s1",
		Timestamp: time.Now(),
	})
	store.Append(hookevents.Event{
		EventName: "PermissionRequest",
		SessionID: "s2",
		Timestamp: time.Now(),
	})

	snap := store.Snapshot()
	require.Len(t, snap, 2)
	assert.Equal(t, logparser.StatusWorking, snap["s1"].Status)
	assert.Equal(t, logparser.StatusBlocked, snap["s2"].Status)
}

func TestHookEventStore_RingBufferTrim(t *testing.T) {
	store := NewHookEventStore()

	// Flooding from a single session trips the per-session cap first,
	// so the total tops out at maxHookEventsPerSession. The global cap
	// still applies and is exercised by the MultiSession test below.
	for i := 0; i < maxHookEvents+20; i++ {
		store.Append(hookevents.Event{
			EventName: "UserPromptSubmit",
			SessionID: "s1",
			Timestamp: time.Now(),
		})
	}

	store.mu.Lock()
	count := len(store.events)
	store.mu.Unlock()
	assert.Equal(t, maxHookEventsPerSession, count, "single-session flood must not exceed per-session cap")
}

func TestHookEventStore_EventsSinceSurvivesSaturation(t *testing.T) {
	store := NewHookEventStore()

	// Saturate the global ring across many sessions (so the per-session cap
	// does not trip first); len(events) pins at maxHookEvents and stops growing.
	for i := 0; i < maxHookEvents; i++ {
		store.Append(hookevents.Event{
			EventName: "UserPromptSubmit",
			SessionID: fmt.Sprintf("s%d", i%200),
			Timestamp: time.Now(),
		})
	}
	store.mu.Lock()
	require.Equal(t, maxHookEvents, len(store.events))
	store.mu.Unlock()

	// Current high-water sequence (a huge "since" returns no events).
	_, seq := store.EventsSince(^uint64(0))

	// A new event after saturation must still be delivered by sequence even
	// though the ring length no longer grows — the bug a length-based cursor
	// would silently miss.
	store.Append(hookevents.Event{EventName: "AgentStopped", AgentName: "agent-x", SessionID: "s1", Timestamp: time.Now()})

	newEvents, newSeq := store.EventsSince(seq)
	require.Greater(t, newSeq, seq)
	found := false
	for _, e := range newEvents {
		if e.EventName == "AgentStopped" && e.AgentName == "agent-x" {
			found = true
		}
	}
	assert.True(t, found, "new AgentStopped must be delivered after ring saturation")
}

func TestHookEventStore_PerSessionCapProtectsOtherSessions(t *testing.T) {
	store := NewHookEventStore()

	// Legitimate session posts a handful of events.
	for i := 0; i < 5; i++ {
		store.Append(hookevents.Event{
			EventName: "UserPromptSubmit",
			SessionID: "legit",
			Timestamp: time.Now(),
		})
	}
	// Abuser floods with far more than the per-session cap.
	for i := 0; i < maxHookEventsPerSession*3; i++ {
		store.Append(hookevents.Event{
			EventName: "UserPromptSubmit",
			SessionID: "flood",
			Timestamp: time.Now(),
		})
	}

	store.mu.Lock()
	var legitCount int
	for _, e := range store.events {
		if e.SessionID == "legit" {
			legitCount++
		}
	}
	store.mu.Unlock()

	assert.Equal(t, 5, legitCount, "legit session must survive a flood from another session")
}

func TestParseHookEventArgs(t *testing.T) {
	evt := ParseHookEventArgs([]string{"PermissionRequest", "s1", "/proj", ""})
	assert.Equal(t, "PermissionRequest", evt.EventName)
	assert.Equal(t, "s1", evt.SessionID)
	assert.Equal(t, "/proj", evt.Cwd)
	assert.False(t, evt.Timestamp.IsZero())
}

func TestParseHookEventArgs_WithNotificationType(t *testing.T) {
	evt := ParseHookEventArgs([]string{"Notification", "s1", "/proj", "idle_prompt"})
	assert.Equal(t, "Notification", evt.EventName)
	assert.Equal(t, "idle_prompt", evt.NotificationType)
}

func TestParseHookEventArgs_Empty(t *testing.T) {
	evt := ParseHookEventArgs(nil)
	assert.Empty(t, evt.EventName)
	assert.Empty(t, evt.SessionID)
}

func TestHookEventStore_Subscribe(t *testing.T) {
	store := NewHookEventStore()
	ch := store.Subscribe()

	store.Append(hookevents.Event{
		EventName: "UserPromptSubmit",
		SessionID: "s1",
		Timestamp: time.Now(),
	})

	select {
	case <-ch:
		// expected
	case <-time.After(time.Second):
		t.Fatal("subscriber should have been notified")
	}
}

func TestHookEventStore_SubscribeCoalesces(t *testing.T) {
	store := NewHookEventStore()
	ch := store.Subscribe()

	// Append twice before reading — only one notification should be buffered.
	store.Append(hookevents.Event{EventName: "Stop", SessionID: "s1", Timestamp: time.Now()})
	store.Append(hookevents.Event{EventName: "Stop", SessionID: "s1", Timestamp: time.Now()})

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected notification")
	}

	// Channel should be empty now.
	select {
	case <-ch:
		t.Fatal("expected no second notification (should coalesce)")
	default:
	}
}

func TestHookEventStore_Unsubscribe(t *testing.T) {
	store := NewHookEventStore()
	ch := store.Subscribe()
	store.Unsubscribe(ch)

	store.Append(hookevents.Event{EventName: "Stop", SessionID: "s1", Timestamp: time.Now()})

	// After Unsubscribe, Append must not deliver to the removed channel.
	select {
	case <-ch:
		t.Fatal("unsubscribed channel should not receive further notifications")
	default:
	}
}

// Concurrent Append/Snapshot drivers for the race detector. The
// existing UnsubscribeRaceAppend test targets the subscriber pathway;
// this test targets the event-ring pathway and confirms Snapshot can
// safely read while many producers append across sessions.
func TestHookEventStore_concurrentAppendAndSnapshot(t *testing.T) {
	store := NewHookEventStore()
	const workers = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for w := 0; w < workers; w++ {
		sessionID := fmt.Sprintf("s-%d", w)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				store.Append(hookevents.Event{
					EventName: "Stop",
					SessionID: sessionID,
					Timestamp: time.Now(),
				})
			}
		}()
	}
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = store.Snapshot()
			}
		}()
	}
	wg.Wait()
}

// TestHookEventStore_UnsubscribeRaceAppend exercises concurrent Append and
// Unsubscribe on the same subscriber channel under -race to confirm no panic
// and no deadlock occurs. Run with: go test -race ./internal/daemon/...
func TestHookEventStore_UnsubscribeRaceAppend(t *testing.T) {
	const iterations = 500
	var wg sync.WaitGroup
	for i := 0; i < iterations; i++ {
		store := NewHookEventStore()
		ch := store.Subscribe()
		// Drain any notifications so the send path in Append always takes the
		// default branch and we exercise the lookup+remove path under race.
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-ch:
				case <-done:
					return
				}
			}
		}()

		wg.Add(2)
		go func() {
			defer wg.Done()
			store.Append(hookevents.Event{EventName: "Stop", SessionID: "s1", Timestamp: time.Now()})
		}()
		go func() {
			defer wg.Done()
			store.Unsubscribe(ch)
		}()
		wg.Wait()
		close(done)
	}
}

func TestHookEventStore_Persists(t *testing.T) {
	var mu sync.Mutex
	var got []hookevents.Event
	store := NewHookEventStore().WithPersistence(func(e hookevents.Event) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})

	evt := hookevents.Event{EventName: "Stop", SessionID: "s1", AgentName: "a1", Timestamp: time.Now()}
	store.Append(evt)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1)
	assert.Equal(t, "a1", got[0].AgentName)
}
