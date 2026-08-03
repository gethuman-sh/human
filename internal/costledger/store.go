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
	Ticket          string      `json:"ticket"`
	HasSpend        bool        `json:"hasSpend"`
	TotalCostUSD    float64     `json:"totalCostUSD"`
	ContextCostUSD  float64     `json:"contextCostUSD"`
	AnswersCostUSD  float64     `json:"answersCostUSD"`
	TotalDurationMs int64       `json:"totalDurationMs"`
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
	out := TicketCost{Ticket: ticket}
	rows, err := s.db.QueryContext(ctx,
		`SELECT stage, model, SUM(input_tokens), SUM(output_tokens), SUM(cache_create_tokens), SUM(cache_read_tokens), SUM(duration_ms)
		 FROM ticket_calls WHERE project = ? AND ticket = ? GROUP BY stage, model`,
		project, ticket)
	if err != nil {
		return out, errors.WrapWithDetails(err, "query ticket cost", "ticket", ticket)
	}
	defer func() { _ = rows.Close() }()

	byStage := map[string]*StageCost{}
	for rows.Next() {
		var stage, model string
		var in, outTok, cc, cr int
		var durMs int64
		if err := rows.Scan(&stage, &model, &in, &outTok, &cc, &cr, &durMs); err != nil {
			return out, errors.WrapWithDetails(err, "scan ticket cost row", "ticket", ticket)
		}
		out.HasSpend = true
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
