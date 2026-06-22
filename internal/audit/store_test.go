package audit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedEvent(t *testing.T, s *Store, kind, key string, now time.Time) Event {
	t.Helper()
	op := MutatingOp{Operation: "create", TrackerKind: kind, Key: key}
	e, err := BuildEvent(now, op, OutcomeSuccess, DecisionContext{Rationale: "why"}, []string{kind, "issue", "create"})
	require.NoError(t, err)
	require.NoError(t, s.Insert(context.Background(), e))
	return e
}

func TestInsertQuery(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	orig := seedEvent(t, s, "jira", "KAN-1", now)

	got, err := s.Query(context.Background(), Filter{Since: now.Add(-time.Hour), Until: now.Add(time.Hour)})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, orig.ID, got[0].ID)
	assert.Equal(t, "KAN-1", got[0].Subject)
	assert.Equal(t, "why", got[0].Data.Decision.Rationale)
	assert.Equal(t, OutcomeSuccess, got[0].Data.Outcome)
}

func TestQueryBySubject(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	seedEvent(t, s, "jira", "KAN-1", now)
	seedEvent(t, s, "jira", "KAN-2", now)

	got, err := s.Query(context.Background(), Filter{
		Since: now.Add(-time.Hour), Until: now.Add(time.Hour), Subject: "KAN-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "KAN-1", got[0].Subject)
}

func TestQueryByTracker(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	seedEvent(t, s, "jira", "KAN-1", now)
	seedEvent(t, s, "linear", "HUM-1", now)

	got, err := s.Query(context.Background(), Filter{
		Since: now.Add(-time.Hour), Until: now.Add(time.Hour), TrackerKind: "linear",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "linear", got[0].Data.Actor.TrackerKind)
}

func TestPrune(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().UTC().Add(-100 * 24 * time.Hour)
	seedEvent(t, s, "jira", "KAN-1", old)

	deleted, err := s.Prune(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	got, err := s.Query(context.Background(), Filter{
		Since: old.Add(-time.Hour), Until: time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestQueryLimit(t *testing.T) {
	s := newTestStore(t)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		// Spread timestamps so newest-first ordering is deterministic.
		seedEvent(t, s, "jira", "KAN-1", base.Add(time.Duration(i)*time.Minute))
	}

	got, err := s.Query(context.Background(), Filter{
		Since: base.Add(-time.Hour), Until: base.Add(time.Hour), Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Newest first: the last seeded event has the latest timestamp.
	assert.True(t, !got[0].Time.Before(got[1].Time))
}
