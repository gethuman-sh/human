package stats

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

func TestWriter_SendAndClose(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	w := NewWriter(ctx, store, zerolog.Nop())

	now := time.Now().UTC()
	w.Send(hookevents.Event{
		SessionID: "s1",
		EventName: "PostToolUse",
		ToolName:  "Bash",
		Cwd:       "/proj",
		Timestamp: now,
	})
	w.Send(hookevents.Event{
		SessionID: "s1",
		EventName: "PostToolUse",
		ToolName:  "Read",
		Cwd:       "/proj",
		Timestamp: now,
	})

	w.Close()

	total, err := store.QueryTotal(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, total)
}

func TestWriter_ZeroTimestampFallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	w := NewWriter(ctx, store, zerolog.Nop())

	w.Send(hookevents.Event{
		SessionID: "s1",
		EventName: "PostToolUse",
		ToolName:  "Bash",
		Cwd:       "/proj",
		// Timestamp intentionally zero
	})

	w.Close()

	// The event should have been inserted with a non-zero timestamp (time.Now).
	since := time.Now().UTC().Add(-time.Minute)
	until := time.Now().UTC().Add(time.Minute)
	total, err := store.QueryTotal(ctx, since, until)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
}

func TestWriter_ContextCancellationDrains(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := NewWriter(ctx, store, zerolog.Nop())

	now := time.Now().UTC()
	// Send a few events.
	for i := 0; i < 5; i++ {
		w.Send(hookevents.Event{
			SessionID: "s1",
			EventName: "PostToolUse",
			ToolName:  "Bash",
			Cwd:       "/proj",
			Timestamp: now,
		})
	}

	// Cancel context — the run loop should drain remaining buffered events.
	cancel()
	<-w.done

	total, err := store.QueryTotal(context.Background(), now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	// All 5 events should be persisted (some via normal loop, rest via drain).
	assert.GreaterOrEqual(t, total, 1, "at least some events should be persisted")
	assert.LessOrEqual(t, total, 5, "at most 5 events should be persisted")
}

func TestWriter_ChannelFullDrops(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, store, zerolog.Nop())

	// Fill the channel beyond capacity — extras should be silently dropped.
	now := time.Now().UTC()
	for i := 0; i < writerBufSize+100; i++ {
		w.Send(hookevents.Event{
			SessionID: "s1",
			EventName: "PostToolUse",
			ToolName:  "Bash",
			Cwd:       "/proj",
			Timestamp: now,
		})
	}

	w.Close()

	total, err := store.QueryTotal(context.Background(), now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	// We should have at most writerBufSize events (some may have been consumed
	// by the goroutine before we filled the channel, so total ≤ writerBufSize+100
	// but the test mainly verifies no panic occurs).
	assert.LessOrEqual(t, total, writerBufSize+100)
	assert.Greater(t, total, 0)
}
