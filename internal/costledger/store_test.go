package costledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/claude"
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

func TestStore_InsertAndRollup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	records := []CallRecord{
		{Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 200, CacheCreateTokens: 50, CacheReadTokens: 900, DurationMs: 1000},
		{Ticket: "SC-1", Stage: "implementation", Model: "claude-opus-4-8", InputTokens: 10, OutputTokens: 20, DurationMs: 2000},
		{Ticket: "SC-1", Stage: "implementation", Model: "claude-sonnet-4-5", InputTokens: 5, OutputTokens: 7, DurationMs: 500},
	}
	for _, r := range records {
		require.NoError(t, s.InsertCall(ctx, r))
	}

	rollup, err := s.TicketCost(ctx, "", "SC-1")
	require.NoError(t, err)
	assert.True(t, rollup.HasSpend)

	var wantTotal, wantCtx, wantAns float64
	for _, r := range records {
		wantTotal += claude.CostUSD(r.Model, r.InputTokens, r.OutputTokens, r.CacheCreateTokens, r.CacheReadTokens)
		wantCtx += claude.CostUSD(r.Model, r.InputTokens, 0, r.CacheCreateTokens, r.CacheReadTokens)
		wantAns += claude.CostUSD(r.Model, 0, r.OutputTokens, 0, 0)
	}
	assert.InDelta(t, wantTotal, rollup.TotalCostUSD, 1e-9)
	assert.InDelta(t, wantCtx, rollup.ContextCostUSD, 1e-9)
	assert.InDelta(t, wantAns, rollup.AnswersCostUSD, 1e-9)
	assert.InDelta(t, wantTotal, rollup.ContextCostUSD+rollup.AnswersCostUSD, 1e-9, "context + answers must equal total")
	assert.Equal(t, int64(3500), rollup.TotalDurationMs)

	byStage := map[string]StageCost{}
	for _, sc := range rollup.Stages {
		byStage[sc.Stage] = sc
	}
	assert.Equal(t, int64(1000), byStage["planning"].DurationMs)
	assert.Equal(t, int64(2500), byStage["implementation"].DurationMs, "both implementation rows accumulate")
	// The longest-running stage sorts first.
	assert.Equal(t, "implementation", rollup.Stages[0].Stage)
}

func TestStore_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "costledger.db")

	s1, err := NewStore(path)
	require.NoError(t, err)
	require.NoError(t, s1.InsertCall(context.Background(), CallRecord{
		Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 200, DurationMs: 1000,
	}))
	require.NoError(t, s1.Close())

	s2, err := NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	rollup, err := s2.TicketCost(context.Background(), "", "SC-1")
	require.NoError(t, err)
	assert.True(t, rollup.HasSpend, "the row persists across a reopen (criterion 2)")
	assert.Greater(t, rollup.TotalCostUSD, 0.0)
}

func TestStore_ReworkAccumulates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	call := CallRecord{Ticket: "SC-1", Stage: "implementation", Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 200, CacheCreateTokens: 50, CacheReadTokens: 900, DurationMs: 1000}

	single, err := s.TicketCost(ctx, "", "SC-1")
	require.NoError(t, err)
	assert.False(t, single.HasSpend)

	for i := 0; i < 3; i++ {
		require.NoError(t, s.InsertCall(ctx, call))
	}
	rollup, err := s.TicketCost(ctx, "", "SC-1")
	require.NoError(t, err)
	oneCost := claude.CostUSD(call.Model, call.InputTokens, call.OutputTokens, call.CacheCreateTokens, call.CacheReadTokens)
	assert.InDelta(t, oneCost*3, rollup.TotalCostUSD, 1e-9, "a ticket built three times reads as three times the cost")
	assert.Equal(t, int64(3000), rollup.TotalDurationMs)
}

func TestStore_NoSpendEmpty(t *testing.T) {
	s := newTestStore(t)
	rollup, err := s.TicketCost(context.Background(), "", "SC-unknown")
	require.NoError(t, err)
	assert.False(t, rollup.HasSpend)
	assert.Zero(t, rollup.TotalCostUSD)
	assert.Zero(t, rollup.TotalDurationMs)
	assert.Nil(t, rollup.Stages)
}

func TestStore_PerProjectIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.InsertCall(ctx, CallRecord{Project: "A", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, DurationMs: 1000}))
	require.NoError(t, s.InsertCall(ctx, CallRecord{Project: "B", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 999, DurationMs: 5000}))

	a, err := s.TicketCost(ctx, "A", "SC-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1000), a.TotalDurationMs, "project A sees only its own rows")

	b, err := s.TicketCost(ctx, "B", "SC-1")
	require.NoError(t, err)
	assert.Equal(t, int64(5000), b.TotalDurationMs, "project B sees only its own rows")
}

func TestStore_Prune(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -RetentionDays-1)
	require.NoError(t, s.InsertCall(ctx, CallRecord{Ticket: "SC-old", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, DurationMs: 1000, StartedAt: old}))
	require.NoError(t, s.InsertCall(ctx, CallRecord{Ticket: "SC-new", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, DurationMs: 1000}))

	n, err := s.Prune(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	oldRollup, err := s.TicketCost(ctx, "", "SC-old")
	require.NoError(t, err)
	assert.False(t, oldRollup.HasSpend, "the aged-out call is gone")
	newRollup, err := s.TicketCost(ctx, "", "SC-new")
	require.NoError(t, err)
	assert.True(t, newRollup.HasSpend, "the recent call remains")
}

func TestStore_CloseNilSafe(t *testing.T) {
	var s *Store
	assert.NoError(t, s.Close())
}

func TestStore_CloseReal(t *testing.T) {
	s, err := NewStore(":memory:")
	require.NoError(t, err)
	assert.NoError(t, s.Close())
}

func TestNewStore_DirCreationFails(t *testing.T) {
	// A regular file standing where a directory must be created makes MkdirAll
	// fail, exercising the wrapped error path.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	_, err := NewStore(filepath.Join(blocker, "sub", "costledger.db"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create cost ledger directory")
}

func TestDefaultDBPath(t *testing.T) {
	p := DefaultDBPath()
	assert.Equal(t, "costledger.db", filepath.Base(p))
	assert.Contains(t, p, ".human")
}
