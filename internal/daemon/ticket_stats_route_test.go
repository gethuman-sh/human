package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/costledger"
)

func decodeTicketStats(t *testing.T, srv *Server, args []string, projectDir string) []costledger.TicketSpend {
	t.Helper()
	resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleTicketStats(conn, args, projectDir) })
	require.Equal(t, 0, resp.ExitCode, resp.Stderr)
	var spend []costledger.TicketSpend
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &spend))
	return spend
}

func TestHandleTicketStats_emptyListWhenNilLedger(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	spend := decodeTicketStats(t, srv, nil, "/proj")
	assert.NotNil(t, spend)
	assert.Empty(t, spend)
}

// The listing is scoped to the requesting project and ranked most expensive
// first; another project's rows with the same keys never leak in.
func TestHandleTicketStats_ranksTheRequestingProjectOnly(t *testing.T) {
	now := time.Now().UTC()
	store := seededCostStore(t, func(s *costledger.Store) {
		ctx := context.Background()
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "/proj", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", OutputTokens: 100, DurationMs: 1000, StartedAt: now}))
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "/proj", Ticket: "SC-2", Stage: "planning", Model: "claude-opus-4-8", OutputTokens: 900, DurationMs: 5000, StartedAt: now}))
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "/other", Ticket: "SC-1", Stage: "planning", Model: "claude-opus-4-8", OutputTokens: 9000, DurationMs: 9000, StartedAt: now}))
	})
	srv := &Server{Logger: zerolog.Nop(), CostLedger: store}

	spend := decodeTicketStats(t, srv, []string{"--range", "7d"}, "/proj")
	require.Len(t, spend, 2)
	assert.Equal(t, "SC-2", spend[0].Ticket)
	assert.Equal(t, 900, spend[0].OutputTokens)
	assert.Equal(t, "SC-1", spend[1].Ticket)
	assert.Equal(t, 100, spend[1].OutputTokens, "the other project's SC-1 must not be summed in")

	limited := decodeTicketStats(t, srv, []string{"--range=7d", "--limit=1"}, "/proj")
	require.Len(t, limited, 1)
	assert.Equal(t, "SC-2", limited[0].Ticket)
}

func TestParseLimitArg(t *testing.T) {
	assert.Equal(t, defaultTicketStatsLimit, parseLimitArg(nil))
	assert.Equal(t, 5, parseLimitArg([]string{"--range", "7d", "--limit", "5"}))
	assert.Equal(t, 5, parseLimitArg([]string{"--limit=5"}))
	assert.Equal(t, defaultTicketStatsLimit, parseLimitArg([]string{"--limit", "0"}))
	assert.Equal(t, defaultTicketStatsLimit, parseLimitArg([]string{"--limit", "many"}))
}

func TestParseProjectArg(t *testing.T) {
	assert.Equal(t, "", parseProjectArg(nil))
	assert.Equal(t, "", parseProjectArg([]string{"--range", "7d"}))
	assert.Equal(t, "/proj", parseProjectArg([]string{"--range", "7d", "--project", "/proj"}))
	assert.Equal(t, "/proj", parseProjectArg([]string{"--project=/proj"}))
}

// TestHandleTicketStats_explicitProjectOverridesConnection pins the fix: the
// "stats tickets" CLI form re-enters the daemon over a fresh connection whose
// own cwd is the daemon process's, not the original caller's project. An
// explicit --project — sent by a caller that already knows its own
// HUMAN_PROJECT_DIR — must win over that connection-derived projectDir, so a
// multi-project daemon answers from the caller's project rather than its own.
func TestHandleTicketStats_explicitProjectOverridesConnection(t *testing.T) {
	now := time.Now().UTC()
	store := seededCostStore(t, func(s *costledger.Store) {
		ctx := context.Background()
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "/caller-project", Ticket: "SC-9", Stage: "planning", Model: "claude-opus-4-8", OutputTokens: 500, DurationMs: 1000, StartedAt: now}))
		require.NoError(t, s.InsertCall(ctx, costledger.CallRecord{Project: "/daemon-cwd", Ticket: "SC-8", Stage: "planning", Model: "claude-opus-4-8", OutputTokens: 700, DurationMs: 1000, StartedAt: now}))
	})
	srv := &Server{Logger: zerolog.Nop(), CostLedger: store}

	// The connection resolved "/daemon-cwd" (the reentrant call's own
	// process cwd), but the caller carried its real project explicitly.
	spend := decodeTicketStats(t, srv, []string{"--range", "7d", "--project", "/caller-project"}, "/daemon-cwd")
	require.Len(t, spend, 1)
	assert.Equal(t, "SC-9", spend[0].Ticket)
}

// TestHandleTicketStats_fallsBackToConnectionWhenNoExplicitProject keeps a
// direct (non-forwarded) caller — which never sends --project — answered
// from the connection-derived project exactly as before.
func TestHandleTicketStats_fallsBackToConnectionWhenNoExplicitProject(t *testing.T) {
	now := time.Now().UTC()
	store := seededCostStore(t, func(s *costledger.Store) {
		require.NoError(t, s.InsertCall(context.Background(), costledger.CallRecord{Project: "/proj", Ticket: "SC-9", Stage: "planning", Model: "claude-opus-4-8", OutputTokens: 500, DurationMs: 1000, StartedAt: now}))
	})
	srv := &Server{Logger: zerolog.Nop(), CostLedger: store}

	spend := decodeTicketStats(t, srv, []string{"--range", "7d"}, "/proj")
	require.Len(t, spend, 1)
	assert.Equal(t, "SC-9", spend[0].Ticket)
}
