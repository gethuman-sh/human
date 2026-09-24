package cmdstats

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/costledger"
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
		TotalCostUSD: 0.5, AnswersCostUSD: 0.2, ContextCostUSD: 0.3, TotalDurationMs: 90_000, Calls: 7, UnmeasuredCalls: 2,
		Stages: []costledger.StageCost{{Stage: "planning", CostUSD: 0.5, AnswersCostUSD: 0.2, ContextCostUSD: 0.3, DurationMs: 90_000}},
	})
	out := buf.String()
	assert.Contains(t, out, "SC-1: $0.50 total (answers $0.20, context $0.30), 1m30s, 7 calls")
	assert.Contains(t, out, "2 of those calls carried no token counts")
	assert.Contains(t, out, "planning")
}

func TestFormatCountAndDuration(t *testing.T) {
	assert.Equal(t, "999", formatCount(999))
	assert.Equal(t, "1.5k", formatCount(1500))
	assert.Equal(t, "2.0M", formatCount(2_000_000))
	assert.Equal(t, "45s", formatDuration(45_000))
	assert.Equal(t, "2m05s", formatDuration(125_000))
	assert.Equal(t, "3h00m", formatDuration(3*3_600_000))
}

func TestTicketsCmd_rejectsUnknownRange(t *testing.T) {
	cmd := buildTicketsCmd()
	cmd.SetArgs([]string{"--range", "1y"})
	cmd.SetOut(&bytes.Buffer{})
	err := cmd.Execute()
	assert.Error(t, err)
}
