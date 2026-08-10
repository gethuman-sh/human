package costledger

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
	_ "modernc.org/sqlite"
)

// RetentionDays bounds the ledger on disk. It is generous — a ticket's whole
// life is the point (SC-2847 criterion 2) — but not unbounded: a ticket idle
// this long has left the board.
const RetentionDays = 180

// CallRecord is one attributed model call's durable footprint: the four token
// classes, the model that priced them, and the call's duration. Raw tokens are
// stored (not a computed dollar figure) so historical calls re-price correctly
// when the single rate card changes (SC-2847 AD3).
type CallRecord struct {
	Project           string
	Ticket            string
	Stage             string
	Model             string
	InputTokens       int
	OutputTokens      int
	CacheCreateTokens int
	CacheReadTokens   int
	DurationMs        int64
	StartedAt         time.Time
}

// StageCost is one stage's priced roll-up (SC-2847 criterion 4).
type StageCost struct {
	Stage          string  `json:"stage"`
	CostUSD        float64 `json:"costUSD"`
	ContextCostUSD float64 `json:"contextCostUSD"`
	AnswersCostUSD float64 `json:"answersCostUSD"`
	DurationMs     int64   `json:"durationMs"`
}

// TicketCost is a ticket's whole-life priced roll-up with the context/answers
// split (criterion 3) and per-stage breakdown (criterion 4). HasSpend is false
// when no call was ever recorded, driving the plain empty state (criterion 5).
type TicketCost struct {
	Ticket string `json:"ticket"`
	// LedgerRead reports that the ledger was actually consulted. Without it a
	// ticket nobody spent on and a ledger that would not open are the same
	// empty answer, and the panel states "no spend recorded for this ticket" —
	// a claim about the ticket made from a fact about the reader (SC-4151 C8).
	LedgerRead      bool    `json:"ledgerRead"`
	HasSpend        bool    `json:"hasSpend"`
	TotalCostUSD    float64 `json:"totalCostUSD"`
	ContextCostUSD  float64 `json:"contextCostUSD"`
	AnswersCostUSD  float64 `json:"answersCostUSD"`
	TotalDurationMs int64   `json:"totalDurationMs"`
	// Calls is how many recorded calls the roll-up is made of, and
	// UnmeasuredCalls how many of them carried no token counts at all — the
	// zero-token rows written before the proxy could read usage off a
	// compressed body (SC-3440). They price at nothing because nothing was
	// measured, not because nothing was spent, and a dollar figure that does
	// not say so asserts a run was free (SC-4151 C7). Two tickets on this
	// board consist of nothing else: SC-1542 (85 calls, 8m40s) and SC-3339
	// (222 calls, 80m37s), both rendering $0.0000 beside a real duration.
	Calls           int         `json:"calls"`
	UnmeasuredCalls int         `json:"unmeasuredCalls"`
	Stages          []StageCost `json:"stages"`
}

// Store is the durable, unbounded, per-project per-ticket ledger.
type Store struct{ db *sql.DB }

// NewStore opens (or creates) the SQLite ledger at dbPath, matching the house
// open/PRAGMA/single-conn pattern (internal/stats). Use ":memory:" in tests.
func NewStore(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create cost ledger directory", "path", dir)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open cost ledger database", "path", dbPath)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, errors.WrapWithDetails(err, "set busy_timeout")
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, errors.WrapWithDetails(err, "set WAL mode")
	}
	s := &Store{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) ensureSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS ticket_calls (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	project             TEXT    NOT NULL DEFAULT '',
	ticket              TEXT    NOT NULL,
	stage               TEXT    NOT NULL DEFAULT '',
	model               TEXT    NOT NULL DEFAULT '',
	input_tokens        INTEGER NOT NULL DEFAULT 0,
	output_tokens       INTEGER NOT NULL DEFAULT 0,
	cache_create_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
	duration_ms         INTEGER NOT NULL DEFAULT 0,
	started_at          DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ticket_calls_ticket ON ticket_calls (project, ticket);
CREATE INDEX IF NOT EXISTS idx_ticket_calls_started ON ticket_calls (started_at);`
	_, err := s.db.Exec(schema)
	if err != nil {
		return errors.WrapWithDetails(err, "ensure cost ledger schema")
	}
	return nil
}

// InsertCall appends one attributed model call. Append-only and unbounded per
// key so a ticket's whole life — including reworks — accumulates (criterion 2).
func (s *Store) InsertCall(ctx context.Context, r CallRecord) error {
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ticket_calls (project, ticket, stage, model, input_tokens, output_tokens, cache_create_tokens, cache_read_tokens, duration_ms, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Project, r.Ticket, r.Stage, r.Model, r.InputTokens, r.OutputTokens, r.CacheCreateTokens, r.CacheReadTokens, r.DurationMs, started.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return errors.WrapWithDetails(err, "insert ticket call", "ticket", r.Ticket)
	}
	return nil
}

// TicketCost prices a ticket's whole-life spend at read time from the single
// rate card, split into answers vs context (criterion 3) and grouped by stage
// (criterion 4). Each token class feeds its own claude.CostUSD argument slot —
// collapsing classes mis-rates by up to 20x.
func (s *Store) TicketCost(ctx context.Context, project, ticket string) (TicketCost, error) {
	out := TicketCost{Ticket: ticket, LedgerRead: true}
	rows, err := s.db.QueryContext(ctx,
		`SELECT stage, model, SUM(input_tokens), SUM(output_tokens), SUM(cache_create_tokens), SUM(cache_read_tokens), SUM(duration_ms),
		        COUNT(*),
		        SUM(CASE WHEN input_tokens + output_tokens + cache_create_tokens + cache_read_tokens = 0 THEN 1 ELSE 0 END)
		 FROM ticket_calls WHERE project = ? AND ticket = ? GROUP BY stage, model`,
		project, ticket)
	if err != nil {
		return out, errors.WrapWithDetails(err, "query ticket cost", "ticket", ticket)
	}
	defer func() { _ = rows.Close() }()

	byStage := map[string]*StageCost{}
	for rows.Next() {
		var stage, model string
		var in, outTok, cc, cr, calls, unmeasured int
		var durMs int64
		if err := rows.Scan(&stage, &model, &in, &outTok, &cc, &cr, &durMs, &calls, &unmeasured); err != nil {
			return out, errors.WrapWithDetails(err, "scan ticket cost row", "ticket", ticket)
		}
		out.HasSpend = true
		out.Calls += calls
		out.UnmeasuredCalls += unmeasured
		cost := claude.CostUSD(model, in, outTok, cc, cr)
		ctxCost := claude.CostUSD(model, in, 0, cc, cr)
		ansCost := claude.CostUSD(model, 0, outTok, 0, 0)

		sc, ok := byStage[stage]
		if !ok {
			sc = &StageCost{Stage: stage}
			byStage[stage] = sc
		}
		sc.CostUSD += cost
		sc.ContextCostUSD += ctxCost
		sc.AnswersCostUSD += ansCost
		sc.DurationMs += durMs

		out.TotalCostUSD += cost
		out.ContextCostUSD += ctxCost
		out.AnswersCostUSD += ansCost
		out.TotalDurationMs += durMs
	}
	if err := rows.Err(); err != nil {
		return out, errors.WrapWithDetails(err, "iterate ticket cost rows", "ticket", ticket)
	}
	for _, sc := range byStage {
		out.Stages = append(out.Stages, *sc)
	}
	sort.Slice(out.Stages, func(i, j int) bool {
		if out.Stages[i].DurationMs != out.Stages[j].DurationMs {
			return out.Stages[i].DurationMs > out.Stages[j].DurationMs
		}
		return out.Stages[i].Stage < out.Stages[j].Stage
	})
	return out, nil
}

// Prune deletes calls older than RetentionDays.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -RetentionDays).Format("2006-01-02 15:04:05")
	res, err := s.db.ExecContext(ctx, `DELETE FROM ticket_calls WHERE started_at < ?`, cutoff)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "prune cost ledger")
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Close closes the database. Safe on a nil store.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// TicketSpend is one ticket's priced spend over a window, ranked for the stats
// view. It carries the answers/context split as tokens as well as dollars,
// because the split is the number that says whether a ticket was expensive for
// a good reason (SC-3497).
type TicketSpend struct {
	Ticket         string  `json:"ticket"`
	CostUSD        float64 `json:"costUSD"`
	ContextCostUSD float64 `json:"contextCostUSD"`
	AnswersCostUSD float64 `json:"answersCostUSD"`
	OutputTokens   int     `json:"outputTokens"`
	ContextTokens  int     `json:"contextTokens"`
	DurationMs     int64   `json:"durationMs"`
}

// TopTicketSpend ranks the tickets that cost the most over a window, most
// expensive first. Pricing happens here, per (ticket, model), for the same
// reason TicketCost prices at read time: the rate card can change, and a stored
// dollar figure would silently keep the old rate.
//
// A row the rate card does not recognise is not unpriced: an unknown family
// prices at the card's fallback — the most expensive row — so it ranks high
// rather than at zero (internal/claude/models.json). What does rank at zero is
// a zero-token row: those written before the proxy read usage off a compressed
// body (SC-3440) carry no tokens to price. They still rank, because they are
// part of the truth about a ticket: it ran, and what it cost is not known.
func (s *Store) TopTicketSpend(ctx context.Context, project string, since, until time.Time, limit int) ([]TicketSpend, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT ticket, model, SUM(input_tokens), SUM(output_tokens), SUM(cache_create_tokens), SUM(cache_read_tokens), SUM(duration_ms)
		 FROM ticket_calls
		 WHERE project = ? AND started_at >= ? AND started_at <= ?
		 GROUP BY ticket, model`,
		project, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query ticket spend", "project", project)
	}
	defer func() { _ = rows.Close() }()

	byTicket := map[string]*TicketSpend{}
	for rows.Next() {
		var ticket, model string
		var in, outTok, cc, cr int
		var durMs int64
		if err := rows.Scan(&ticket, &model, &in, &outTok, &cc, &cr, &durMs); err != nil {
			return nil, errors.WrapWithDetails(err, "scan ticket spend row")
		}
		ts, ok := byTicket[ticket]
		if !ok {
			ts = &TicketSpend{Ticket: ticket}
			byTicket[ticket] = ts
		}
		ts.CostUSD += claude.CostUSD(model, in, outTok, cc, cr)
		ts.ContextCostUSD += claude.CostUSD(model, in, 0, cc, cr)
		ts.AnswersCostUSD += claude.CostUSD(model, 0, outTok, 0, 0)
		ts.OutputTokens += outTok
		ts.ContextTokens += in + cc + cr
		ts.DurationMs += durMs
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "iterate ticket spend rows")
	}

	out := make([]TicketSpend, 0, len(byTicket))
	for _, ts := range byTicket {
		out = append(out, *ts)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		// Cost ties on unpriced rows, so fall back to the work actually done and
		// then to the key, keeping the order stable rather than map-random.
		if out[i].DurationMs != out[j].DurationMs {
			return out[i].DurationMs > out[j].DurationMs
		}
		return out[i].Ticket < out[j].Ticket
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
