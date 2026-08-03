package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/costledger"
)

func seededCostStore(t *testing.T, seed func(*costledger.Store)) *costledger.Store {
	t.Helper()
	store, err := costledger.NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if seed != nil {
		seed(store)
	}
	return store
}

func decodeTicketCost(t *testing.T, srv *Server, key string) costledger.TicketCost {
	t.Helper()
	resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleTicketCost(conn, []string{key}) })
	require.Equal(t, 0, resp.ExitCode, resp.Stderr)
	var rollup costledger.TicketCost
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &rollup))
	return rollup
}

func TestHandleTicketCost_emptyWhenNilLedger(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	rollup := decodeTicketCost(t, srv, "SC-1")
	assert.False(t, rollup.HasSpend)
	assert.Equal(t, "SC-1", rollup.Ticket)
}

func TestHandleTicketCost_withLedger(t *testing.T) {
	ctx := context.Background()
	store := seededCostStore(t, func(s *costledger.Store) {
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{
			Project: "A", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8",
			InputTokens: 100, OutputTokens: 200, DurationMs: 1000,
		}))
	})
	srv := &Server{Logger: zerolog.Nop(), CostLedger: store, CostLedgerProject: func(string) string { return "A" }}

	rollup := decodeTicketCost(t, srv, "SC-1")
	assert.True(t, rollup.HasSpend)
	want, err := store.TicketCost(ctx, "A", "SC-1")
	require.NoError(t, err)
	assert.InDelta(t, want.TotalCostUSD, rollup.TotalCostUSD, 1e-9)
	assert.Equal(t, want.TotalDurationMs, rollup.TotalDurationMs)
}

// TestHandleTicketCost_projectPerTicket proves the read side resolves the
// project per ticket, not a single board-wide default: two tickets seeded under
// different projects each read back only their own project's rows (SC-2847 AD5).
func TestHandleTicketCost_projectPerTicket(t *testing.T) {
	ctx := context.Background()
	store := seededCostStore(t, func(s *costledger.Store) {
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "A", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, DurationMs: 1000}))
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "B", Ticket: "SC-2", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 100, DurationMs: 7000}))
		// A decoy: SC-1 also exists under B with a huge duration, which a
		// board-wide default resolver would wrongly surface for SC-1.
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "B", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", InputTokens: 999, DurationMs: 99000}))
	})
	perTicket := map[string]string{"SC-1": "A", "SC-2": "B"}
	srv := &Server{Logger: zerolog.Nop(), CostLedger: store, CostLedgerProject: func(k string) string { return perTicket[k] }}

	one := decodeTicketCost(t, srv, "SC-1")
	assert.Equal(t, int64(1000), one.TotalDurationMs, "SC-1 reads project A only, not B's decoy row")

	two := decodeTicketCost(t, srv, "SC-2")
	assert.Equal(t, int64(7000), two.TotalDurationMs, "SC-2 reads project B only")
}
