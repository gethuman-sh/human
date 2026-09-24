package daemon

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/gethuman-sh/human/internal/costledger"
)

// defaultTicketStatsLimit bounds the ranked list when the caller names no
// limit. Wider than the stats view's 12: a terminal scrolls, a panel does not.
const defaultTicketStatsLimit = 20

// handleTicketStats returns the tickets that cost the most over the range,
// scoped to the project the request came from. That is the same directory the
// write side stores (initCostLedger resolves a ticket to entry.Dir), so a
// listing and the board's card detail read one record. A nil ledger yields an
// empty list — the degrade-to-empty contract of the other read routes.
func (s *Server) handleTicketStats(conn net.Conn, args []string, projectDir string) {
	spend := []costledger.TicketSpend{}
	if s.CostLedger != nil {
		now := time.Now().UTC()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := s.CostLedger.TopTicketSpend(ctx, projectDir, rangeSince(parseRangeArg(args), now), now, parseLimitArg(args))
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		if rows != nil {
			spend = rows
		}
	}
	data, err := json.Marshal(spend)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	_ = json.NewEncoder(conn).Encode(Response{Stdout: string(data) + "\n"})
}

// parseLimitArg reads --limit the way parseRangeArg reads --range. Anything
// that is not a positive integer falls back to the default rather than
// failing the request: the limit shapes the answer, it is not the question.
func parseLimitArg(args []string) int {
	for i := 0; i < len(args); i++ {
		name, value, consumed := auditFlagValue(args, i)
		if name != "--limit" {
			if name == "" {
				continue
			}
			i += consumed
			continue
		}
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			return n
		}
		return defaultTicketStatsLimit
	}
	return defaultTicketStatsLimit
}
