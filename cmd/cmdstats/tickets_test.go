package cmdstats

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/env"
)

func TestRenderTicketSpend_empty(t *testing.T) {
	var buf bytes.Buffer
	renderTicketSpend(&buf, "7d", nil)
	assert.Contains(t, buf.String(), "no ticket spend recorded in the last 7d")
}

func TestRenderTicketSpend_rows(t *testing.T) {
	var buf bytes.Buffer
	renderTicketSpend(&buf, "7d", []costledger.TicketSpend{
		{Ticket: "SC-2", CostUSD: 12.5, AnswersCostUSD: 4.25, ContextCostUSD: 8.25, OutputTokens: 420_453, ContextTokens: 56_413_827, DurationMs: 3_725_000},
	})
	out := buf.String()
	assert.Contains(t, out, "TICKET")
	assert.Contains(t, out, "SC-2")
	assert.Contains(t, out, "$12.50")
	assert.Contains(t, out, "420.5k")
	assert.Contains(t, out, "56.4M")
	assert.Contains(t, out, "1h02m")
}

// The list form shares formatUSD with the single-key form; a sub-dollar
// ticket must not round to "$0.00" here either.
func TestRenderTicketSpend_subDollarPrecision(t *testing.T) {
	var buf bytes.Buffer
	renderTicketSpend(&buf, "7d", []costledger.TicketSpend{
		{Ticket: "SC-3", CostUSD: 0.0042, AnswersCostUSD: 0.0012, ContextCostUSD: 0.003, OutputTokens: 10, ContextTokens: 20, DurationMs: 1_000},
	})
	out := buf.String()
	assert.Contains(t, out, "$0.0042")
	assert.NotContains(t, out, "$0.00 ")
}

// The two empty states must read differently: one is about the reader, the
// other about the ticket (SC-4151 C8).
func TestRenderTicketCost_emptyStates(t *testing.T) {
	var buf bytes.Buffer
	renderTicketCost(&buf, costledger.TicketCost{Ticket: "SC-1"})
	assert.Contains(t, buf.String(), "not consulted")

	buf.Reset()
	renderTicketCost(&buf, costledger.TicketCost{Ticket: "SC-1", LedgerRead: true})
	assert.Contains(t, buf.String(), "no spend recorded for this ticket")
}

func TestRenderTicketCost_stages(t *testing.T) {
	var buf bytes.Buffer
	renderTicketCost(&buf, costledger.TicketCost{
		Ticket: "SC-1", LedgerRead: true, HasSpend: true,
		TotalCostUSD: 12.5, AnswersCostUSD: 4.2, ContextCostUSD: 8.3, TotalDurationMs: 90_000, Calls: 7, UnmeasuredCalls: 2, FailedCalls: 3,
		Stages: []costledger.StageCost{{Stage: "planning", CostUSD: 12.5, AnswersCostUSD: 4.2, ContextCostUSD: 8.3, DurationMs: 90_000}},
	})
	out := buf.String()
	assert.Contains(t, out, "SC-1: $12.50 total (answers $4.20, context $8.30), 1m30s, 7 calls")
	assert.Contains(t, out, "2 of those calls carried no token counts")
	assert.Contains(t, out, "3 further calls failed and cost nothing")
	assert.Contains(t, out, "planning")
}

// A sub-dollar total must not round to the same "$0.00" the card detail
// refuses to show — formatUSD matches fmtUSD's precision (SC-5516 review).
func TestRenderTicketCost_subDollarPrecisionMatchesCardDetail(t *testing.T) {
	var buf bytes.Buffer
	renderTicketCost(&buf, costledger.TicketCost{
		Ticket: "SC-1", LedgerRead: true, HasSpend: true,
		TotalCostUSD: 0.0042, AnswersCostUSD: 0.0012, ContextCostUSD: 0.003, TotalDurationMs: 90_000, Calls: 3,
	})
	out := buf.String()
	assert.Contains(t, out, "$0.0042 total (answers $0.0012, context $0.0030)")
	assert.NotContains(t, out, "$0.00 ")
}

// When every call in the roll-up is unmeasured, the ticket-cost renderer must
// refuse to price it exactly as the card detail does (board-detail.ts
// buildCostSection, SC-4151 C7): no total, no answers/context split, no
// per-stage dollars — duration only. SC-1542 (85 calls, 8m40s) and SC-3339
// (222 calls, 80m37s) are made of nothing else.
func TestRenderTicketCost_allUnmeasuredPrintsNoDollarFigures(t *testing.T) {
	var buf bytes.Buffer
	renderTicketCost(&buf, costledger.TicketCost{
		Ticket: "SC-1542", LedgerRead: true, HasSpend: true,
		TotalDurationMs: 520_000, Calls: 85, UnmeasuredCalls: 85,
		Stages: []costledger.StageCost{{Stage: "planning", DurationMs: 520_000}},
	})
	out := buf.String()
	assert.Contains(t, out, "SC-1542: cost not measured, 8m40s, 85 calls")
	assert.Contains(t, out, "planning")
	assert.Contains(t, out, "8m40s")
	assert.NotContains(t, out, "$")
}

func TestFormatCountAndDuration(t *testing.T) {
	assert.Equal(t, "999", formatCount(999))
	assert.Equal(t, "1.5k", formatCount(1500))
	assert.Equal(t, "2.0M", formatCount(2_000_000))
	assert.Equal(t, "45s", formatDuration(45_000))
	assert.Equal(t, "2m05s", formatDuration(125_000))
	assert.Equal(t, "3h00m", formatDuration(3*3_600_000))
}

// A count just under the M cutoff must not round up to "1000.0k" — the same
// overflow the M cutoff exists to avoid, one unit down.
func TestFormatCount_noRoundingOverflowAtMBoundary(t *testing.T) {
	assert.Equal(t, "1.0M", formatCount(999_999))
	assert.Equal(t, "1.0M", formatCount(999_950))
	assert.Equal(t, "999.9k", formatCount(999_949))
}

func TestTicketsCmd_rejectsUnknownRange(t *testing.T) {
	cmd := buildTicketsCmd()
	cmd.SetArgs([]string{"--range", "1y"})
	cmd.SetOut(&bytes.Buffer{})
	err := cmd.Execute()
	assert.Error(t, err)
}

// The KEY form never uses --range, so an invalid one must not block it.
func TestTicketsCmd_ignoresInvalidRangeWithKey(t *testing.T) {
	startFakeDaemon(t, daemon.Response{
		Stdout: `{"ticket":"SC-1","ledgerRead":true,"hasSpend":true,"totalCostUSD":0.5,"calls":3,"totalDurationMs":90000}` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"SC-1", "--range", "1y"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "SC-1:")
}

// startCapturingDaemon behaves like startFakeDaemon but records the args of
// every request it serves, so a dispatch test can pin which route the
// command actually took rather than only what it rendered.
func startCapturingDaemon(t *testing.T, resp daemon.Response) *[]string {
	t.Helper()
	var captured []string

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req daemon.Request
			_ = json.NewDecoder(conn).Decode(&req)
			captured = req.Args
			_ = json.NewEncoder(conn).Encode(resp)
			_ = conn.Close()
		}
	}()

	original := connect
	connect = func() (*daemon.Client, error) {
		return daemon.NewClient(daemon.DaemonInfo{Addr: ln.Addr().String(), Token: "test-token"})
	}
	t.Cleanup(func() { connect = original })
	return &captured
}

// No KEY argument must route to the ranked list (ticket-stats), carrying
// --range and --limit — not the single-ticket rollup.
func TestTicketsCmd_listFormRoutesToTicketStats(t *testing.T) {
	captured := startCapturingDaemon(t, daemon.Response{
		Stdout: `[{"ticket":"SC-2","costUSD":12.5,"durationMs":90000}]` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--range=30d", "--limit=5"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "SC-2")
	assert.Contains(t, out.String(), "$12.50")
	require.NotNil(t, *captured)
	assert.Equal(t, []string{"ticket-stats", "--range", "30d", "--limit", "5"}, *captured)
}

// When this command's own context carries HUMAN_PROJECT_DIR — the case where
// "stats tickets" is itself a forwarded call already running inside the
// daemon — the list form must carry it explicitly as --project, so the
// reentrant connection QueryTicketSpend opens does not fall back to
// resolving the daemon's own cwd as the project (SC-5516 review).
func TestTicketsCmd_listFormCarriesProjectFromContext(t *testing.T) {
	captured := startCapturingDaemon(t, daemon.Response{
		Stdout: `[]` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--range=30d", "--limit=5"})

	ctx := env.WithEnv(context.Background(), map[string]string{"HUMAN_PROJECT_DIR": "/caller-project"})
	require.NoError(t, cmd.ExecuteContext(ctx))
	require.NotNil(t, *captured)
	assert.Equal(t, []string{"ticket-stats", "--range", "30d", "--limit", "5", "--project", "/caller-project"}, *captured)
}

// A direct (non-forwarded) invocation has no per-request env map on its
// context, so it must send no --project and rely on the connection's own
// project resolution exactly as before.
func TestTicketsCmd_listFormOmitsProjectWithoutContext(t *testing.T) {
	t.Setenv("HUMAN_PROJECT_DIR", "") // env.Lookup falls back to os.Getenv; pin it absent
	captured := startCapturingDaemon(t, daemon.Response{
		Stdout: `[]` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--range=30d", "--limit=5"})

	require.NoError(t, cmd.Execute())
	require.NotNil(t, *captured)
	assert.Equal(t, []string{"ticket-stats", "--range", "30d", "--limit", "5"}, *captured)
}

// A JSON flag on the list form must decode as a slice of TicketSpend.
func TestTicketsCmd_listFormJSON(t *testing.T) {
	startFakeDaemon(t, daemon.Response{
		Stdout: `[{"ticket":"SC-2","costUSD":12.5,"durationMs":90000}]` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json"})

	require.NoError(t, cmd.Execute())
	var spend []costledger.TicketSpend
	require.NoError(t, json.Unmarshal(out.Bytes(), &spend))
	require.Len(t, spend, 1)
	assert.Equal(t, "SC-2", spend[0].Ticket)
}

// A KEY argument must route to the single-ticket rollup (ticket-cost),
// ignoring --range/--limit — not the ranked list.
func TestTicketsCmd_keyFormRoutesToTicketCost(t *testing.T) {
	captured := startCapturingDaemon(t, daemon.Response{
		Stdout: `{"ticket":"SC-1","ledgerRead":true,"hasSpend":true,"totalCostUSD":0.5,"calls":3,"totalDurationMs":90000}` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"SC-1"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "SC-1: $0.5000 total")
	require.NotNil(t, *captured)
	assert.Equal(t, []string{"ticket-cost", "SC-1"}, *captured)
}

// A JSON flag on the KEY form must decode as a single TicketCost, not a list.
func TestTicketsCmd_keyFormJSON(t *testing.T) {
	startFakeDaemon(t, daemon.Response{
		Stdout: `{"ticket":"SC-1","ledgerRead":true,"hasSpend":true,"totalCostUSD":0.5,"calls":3,"totalDurationMs":90000}` + "\n",
	})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"SC-1", "--json"})

	require.NoError(t, cmd.Execute())
	var rollup costledger.TicketCost
	require.NoError(t, json.Unmarshal(out.Bytes(), &rollup))
	assert.Equal(t, "SC-1", rollup.Ticket)
	assert.True(t, rollup.HasSpend)
}

// A daemon error on either form must surface as a command error, not an
// empty/zero render that reads like "nothing spent".
func TestTicketsCmd_daemonError(t *testing.T) {
	startFakeDaemon(t, daemon.Response{ExitCode: 1, Stderr: "cost ledger database is locked"})

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to query ticket statistics")

	startFakeDaemon(t, daemon.Response{ExitCode: 1, Stderr: "cost ledger database is locked"})
	cmd = buildTicketsCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"SC-1"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to query ticket cost")
}

// With no daemon at all, either form guides and exits cleanly rather than
// failing.
func TestTicketsCmd_noDaemon(t *testing.T) {
	withoutDaemon(t)

	cmd := buildTicketsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"SC-1"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), noDaemonMsg)
}
