package audit

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriterPersistsOnSend(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	w := NewWriter(ctx, store, zerolog.Nop())

	now := time.Now().UTC()
	op := MutatingOp{Operation: "create", TrackerKind: "jira", Key: "KAN-1"}
	e, err := BuildEvent(now, op, OutcomeSuccess, DecisionContext{}, []string{"jira", "issue", "create"})
	require.NoError(t, err)
	w.Send(e)

	w.Close()

	got, err := store.Query(ctx, Filter{Since: now.Add(-time.Hour), Until: now.Add(time.Hour)})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, e.ID, got[0].ID)
}

func TestWriterZeroTimestampFallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	w := NewWriter(ctx, store, zerolog.Nop())

	op := MutatingOp{Operation: "create", TrackerKind: "jira"}
	e, err := BuildEvent(time.Time{}, op, OutcomeSuccess, DecisionContext{}, nil)
	require.NoError(t, err)
	e.Time = time.Time{} // force zero so the writer fallback is exercised
	w.Send(e)

	w.Close()

	since := time.Now().UTC().Add(-time.Minute)
	until := time.Now().UTC().Add(time.Minute)
	got, err := store.Query(ctx, Filter{Since: since, Until: until})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestWriterDropsWhenFull(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewWriter(ctx, store, zerolog.Nop())

	now := time.Now().UTC()
	op := MutatingOp{Operation: "create", TrackerKind: "jira", Key: "KAN-1"}
	for i := 0; i < writerBufSize+100; i++ {
		e, err := BuildEvent(now, op, OutcomeSuccess, DecisionContext{}, nil)
		require.NoError(t, err)
		w.Send(e) // must not panic when the channel is full
	}

	w.Close()

	got, err := store.Query(ctx, Filter{Since: now.Add(-time.Hour), Until: now.Add(time.Hour), Limit: writerBufSize + 100})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(got), writerBufSize+100)
	assert.Greater(t, len(got), 0)
}

func TestWriterCloseIdempotent(t *testing.T) {
	store := newTestStore(t)
	w := NewWriter(context.Background(), store, zerolog.Nop())
	w.Close()
	w.Close() // second Close must not panic
}
