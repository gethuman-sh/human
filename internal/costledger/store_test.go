package costledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testModel is a model the rate card prices, so a test asserting on dollars is
// asserting on pricing rather than on a zero.
const testModel = "claude-opus-4-8"

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

// TopTicketSpend ranks by money, not by call count or recency: the panel exists
// to answer "where did this range's spend go", and the most expensive ticket is
// the answer even when a chattier one made more calls.
func TestTopTicketSpend_ranksByCostAndHonoursTheWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Cheap but chatty: many calls, few tokens.
	for i := 0; i < 5; i++ {
		require.NoError(t, s.InsertCall(ctx, CallRecord{
			Project: "p", Ticket: "SC-CHATTY", Stage: "implementation", Model: testModel,
			InputTokens: 10, OutputTokens: 10, DurationMs: 100, StartedAt: now.Add(-time.Hour),
		}))
	}
	// Expensive: one call, a lot of context.
	require.NoError(t, s.InsertCall(ctx, CallRecord{
		Project: "p", Ticket: "SC-COSTLY", Stage: "verification", Model: testModel,
		InputTokens: 5000, OutputTokens: 2000, CacheReadTokens: 900000, DurationMs: 5000,
		StartedAt: now.Add(-time.Hour),
	}))
	// Outside the window: must not appear at all.
	require.NoError(t, s.InsertCall(ctx, CallRecord{
		Project: "p", Ticket: "SC-OLD", Stage: "implementation", Model: testModel,
		InputTokens: 999999, OutputTokens: 999999, DurationMs: 9999,
		StartedAt: now.Add(-72 * time.Hour),
	}))
	// Another project's ticket, same window: never summed in (AD5).
	require.NoError(t, s.InsertCall(ctx, CallRecord{
		Project: "other", Ticket: "SC-COSTLY", Stage: "implementation", Model: testModel,
		InputTokens: 777777, OutputTokens: 777777, DurationMs: 7777, StartedAt: now.Add(-time.Hour),
	}))

	got, err := s.TopTicketSpend(ctx, "p", now.Add(-24*time.Hour), now, 10)
	require.NoError(t, err)

	require.Len(t, got, 2, "the stale row and the other project's row are both excluded")
	assert.Equal(t, "SC-COSTLY", got[0].Ticket, "most expensive first, not most calls")
	assert.Equal(t, "SC-CHATTY", got[1].Ticket)
	assert.Greater(t, got[0].CostUSD, got[1].CostUSD)
	assert.Equal(t, 905000, got[0].ContextTokens, "input + cache-create + cache-read")
	assert.Equal(t, 2000, got[0].OutputTokens)
	assert.InDelta(t, got[0].CostUSD, got[0].ContextCostUSD+got[0].AnswersCostUSD, 1e-9,
		"the split must account for the whole cost")
}

// A ticket whose rows carry no tokens — every row written before the proxy read
// usage off a compressed body (SC-3440) — still ranks, at zero. Dropping it
// would hide that the work ran at all.
func TestTopTicketSpend_keepsUnpricedTickets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, s.InsertCall(ctx, CallRecord{
		Project: "p", Ticket: "SC-BLIND", Stage: "prreview", DurationMs: 6387000, StartedAt: now.Add(-time.Hour),
	}))

	got, err := s.TopTicketSpend(ctx, "p", now.Add(-24*time.Hour), now, 10)
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "SC-BLIND", got[0].Ticket)
	assert.Zero(t, got[0].CostUSD)
	assert.Equal(t, int64(6387000), got[0].DurationMs, "the time it took is still known")
}

// The limit caps the panel, keeping the most expensive rather than an arbitrary
// slice of the map.
func TestTopTicketSpend_limitKeepsTheMostExpensive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, tokens := range []int{100, 5000, 200, 90000, 300} {
		require.NoError(t, s.InsertCall(ctx, CallRecord{
			Project: "p", Ticket: fmt.Sprintf("SC-%d", i), Stage: "implementation", Model: testModel,
			OutputTokens: tokens, DurationMs: 10, StartedAt: now.Add(-time.Hour),
		}))
	}

	got, err := s.TopTicketSpend(ctx, "p", now.Add(-24*time.Hour), now, 2)
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, "SC-3", got[0].Ticket, "90000 output tokens is the most expensive")
	assert.Equal(t, "SC-1", got[1].Ticket)
}

// TestStore_UnmeasuredCallsCounted covers the SC-4151 C7 case: calls recorded
// with no token counts at all (the SC-3440 zero-token rows) price at nothing
// because nothing was measured. The roll-up must say how many, so the reader is
// never shown a dollar figure that means "not known".
func TestStore_UnmeasuredCallsCounted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// The shape of SC-3339: every call recorded, none measured.
	for range 3 {
		require.NoError(t, s.InsertCall(ctx, CallRecord{Ticket: "SC-UNMEASURED", Stage: "implementation", DurationMs: 1000}))
	}
	rollup, err := s.TicketCost(ctx, "", "SC-UNMEASURED")
	require.NoError(t, err)
	assert.True(t, rollup.HasSpend, "calls were recorded")
	assert.True(t, rollup.LedgerRead, "the ledger answered")
	assert.Equal(t, 3, rollup.Calls)
	assert.Equal(t, 3, rollup.UnmeasuredCalls, "no call carried tokens")
	assert.Zero(t, rollup.TotalCostUSD, "nothing to price")
	assert.Equal(t, int64(3000), rollup.TotalDurationMs, "the time is real even when the cost is unknown")
}

func TestStore_PartialMeasurementGap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.InsertCall(ctx, CallRecord{Ticket: "SC-MIXED", Stage: "planning", Model: testModel, InputTokens: 100, OutputTokens: 200, DurationMs: 1000}))
	require.NoError(t, s.InsertCall(ctx, CallRecord{Ticket: "SC-MIXED", Stage: "planning", Model: testModel, DurationMs: 500}))

	rollup, err := s.TicketCost(ctx, "", "SC-MIXED")
	require.NoError(t, err)
	assert.Equal(t, 2, rollup.Calls)
	assert.Equal(t, 1, rollup.UnmeasuredCalls)
	assert.Positive(t, rollup.TotalCostUSD, "the measured call still prices")
}

// TestTicketCost_ZeroValueIsNotAnAnswer pins the C8 distinction: the zero value
// must not claim the ledger was consulted, because handleTicketCost returns it
// verbatim when there is no ledger to consult.
func TestTicketCost_ZeroValueIsNotAnAnswer(t *testing.T) {
	assert.False(t, TicketCost{Ticket: "SC-1"}.LedgerRead)
}
