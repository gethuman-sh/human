package cmdstats

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/env"
)

// defaultTicketLimit is the list length when the caller names none. It is
// sent explicitly so the daemon's own default never decides the answer.
const defaultTicketLimit = 20

func buildTicketsCmd() *cobra.Command {
	var (
		rng    string
		limit  int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "tickets [KEY]",
		Short: "Show what tickets cost in tokens, dollars and time — ranked over a range, or one ticket per stage",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// --range only ever reaches the list form's query; a KEY argument
			// never uses it, so an invalid value must not block the single-
			// ticket rollup that ignores it.
			if len(args) == 0 && !isValidRange(rng) {
				return errors.WithDetails("unknown range", "range", rng, "valid", validRanges)
			}
			client, err := connectDaemon(cmd.OutOrStdout())
			if err != nil {
				return nil // guidance already printed; an unreachable daemon is not a failure
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				rollup, err := client.GetTicketCost(args[0])
				if err != nil {
					return errors.WrapWithDetails(err, "failed to query ticket cost", "key", args[0])
				}
				if asJSON {
					return writeJSON(out, rollup)
				}
				renderTicketCost(out, rollup)
				return nil
			}
			// HUMAN_PROJECT_DIR is set on this command's context only when it
			// is itself already running forwarded, inside the daemon (a
			// multi-project install) — the case where QueryTicketSpend's own
			// reentrant call would otherwise derive the wrong project from
			// the daemon's cwd. A direct, non-forwarded run has no such
			// value and sends "", leaving the connection-derived project in
			// place.
			project := env.Lookup(cmd.Context(), "HUMAN_PROJECT_DIR")
			spend, err := client.QueryTicketSpend(rng, limit, project)
			if err != nil {
				return errors.WrapWithDetails(err, "failed to query ticket statistics")
			}
			if asJSON {
				return writeJSON(out, spend)
			}
			renderTicketSpend(out, rng, spend)
			return nil
		},
	}
	cmd.Flags().StringVar(&rng, "range", "7d", "time window: 24h, 7d or 30d (list form only)")
	cmd.Flags().IntVar(&limit, "limit", defaultTicketLimit, "how many tickets to list, most expensive first (list form only)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw JSON")
	return cmd
}

// renderTicketSpend prints one line per ticket, most expensive first. The
// answers/context split comes before the raw tokens because it is the number
// that says whether a ticket was expensive for a good reason.
func renderTicketSpend(out io.Writer, rng string, spend []costledger.TicketSpend) {
	if len(spend) == 0 {
		_, _ = fmt.Fprintf(out, "no ticket spend recorded in the last %s\n", rng)
		return
	}
	_, _ = fmt.Fprintf(out, "%-12s  %9s  %9s  %9s  %11s  %12s  %s\n", "TICKET", "COST", "ANSWERS", "CONTEXT", "OUTPUT TOK", "CONTEXT TOK", "TIME")
	for _, s := range spend {
		_, _ = fmt.Fprintf(out, "%-12s  %9s  %9s  %9s  %11s  %12s  %s\n",
			s.Ticket, formatUSD(s.CostUSD), formatUSD(s.AnswersCostUSD), formatUSD(s.ContextCostUSD),
			formatCount(s.OutputTokens), formatCount(s.ContextTokens), formatDuration(s.DurationMs))
	}
}

// renderTicketCost prints one ticket's whole-life roll-up and its stages. The
// two empty states are kept apart: a ledger that was not consulted is a fact
// about the reader, a ticket nobody spent on is a fact about the ticket.
//
// A third state sits below HasSpend: every recorded call priced at zero
// because none carried token counts (SC-3440). The card detail refuses to
// call that a cost at all — no total, no answers/context split, no per-stage
// dollars (board-detail.ts buildCostSection, SC-4151 C7) — because a dollar
// figure there asserts the run was free. This renderer must refuse the same
// way, or the two surfaces answer "what did this ticket cost" differently.
func renderTicketCost(out io.Writer, c costledger.TicketCost) {
	if !c.LedgerRead {
		_, _ = fmt.Fprintf(out, "%s: cost ledger not consulted (the daemon has no ledger open)\n", c.Ticket)
		return
	}
	if !c.HasSpend {
		_, _ = fmt.Fprintf(out, "%s: no spend recorded for this ticket\n", c.Ticket)
		return
	}
	allUnmeasured := c.Calls > 0 && c.UnmeasuredCalls == c.Calls
	if allUnmeasured {
		_, _ = fmt.Fprintf(out, "%s: cost not measured, %s, %d calls, none of which carried token counts\n",
			c.Ticket, formatDuration(c.TotalDurationMs), c.Calls)
	} else {
		_, _ = fmt.Fprintf(out, "%s: %s total (answers %s, context %s), %s, %d calls\n",
			c.Ticket, formatUSD(c.TotalCostUSD), formatUSD(c.AnswersCostUSD), formatUSD(c.ContextCostUSD), formatDuration(c.TotalDurationMs), c.Calls)
		if c.UnmeasuredCalls > 0 {
			_, _ = fmt.Fprintf(out, "%d of those calls carried no token counts and price at nothing, not at zero cost\n", c.UnmeasuredCalls)
		}
	}
	if len(c.Stages) == 0 {
		return
	}
	if allUnmeasured {
		_, _ = fmt.Fprintf(out, "%-16s  %s\n", "STAGE", "TIME")
		for _, s := range c.Stages {
			_, _ = fmt.Fprintf(out, "%-16s  %s\n", s.Stage, formatDuration(s.DurationMs))
		}
		return
	}
	_, _ = fmt.Fprintf(out, "%-16s  %9s  %9s  %9s  %s\n", "STAGE", "COST", "ANSWERS", "CONTEXT", "TIME")
	for _, s := range c.Stages {
		_, _ = fmt.Fprintf(out, "%-16s  %9s  %9s  %9s  %s\n", s.Stage, formatUSD(s.CostUSD), formatUSD(s.AnswersCostUSD), formatUSD(s.ContextCostUSD), formatDuration(s.DurationMs))
	}
}

// formatUSD matches the card detail's fmtUSD precision (board-detail.ts):
// four decimals below a dollar so a few cents never rounds to "$0.00", two
// above it. The two renderers price the same ledger and must read the same.
func formatUSD(usd float64) string {
	if usd < 1 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// formatCount renders a token count in the unit a person would read: 1.2k, 56M.
// The M cutoff is 999_950, not 1_000_000: below that a count still rounds up
// to "1000.0k" at one decimal, which is the same overflow in a smaller unit.
func formatCount(n int) string {
	switch {
	case n >= 999_950:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatDuration renders elapsed time to the minute above an hour, to the
// second below it.
func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
