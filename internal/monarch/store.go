package monarch

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/gethuman-sh/human/errors"
	_ "modernc.org/sqlite"
)

// RetentionDays is the rolling window monarch keeps. Deliberately shorter than
// audit's 90: monarch is a live operations console, not an accountability
// trail, so a stale event past two weeks has no value.
const RetentionDays = 14

// dbTimeFormat is a fixed-width timestamp layout so lexical string comparison
// equals chronological ordering for range and prune filters.
const dbTimeFormat = "2006-01-02 15:04:05"

// Store persists monarch events in SQLite. The full event is stored as a JSON
// envelope alongside decomposed, indexed columns so the work-board and burn
// aggregations stay fast while the envelope round-trips intact.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite database at dbPath and ensures the
// schema is up to date. Use ":memory:" for in-memory databases in tests.
func NewStore(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create monarch directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open monarch database", "path", dbPath)
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
		CREATE TABLE IF NOT EXISTS monarch_events (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			type          TEXT NOT NULL,
			team          TEXT NOT NULL DEFAULT '',
			daemon_id     TEXT NOT NULL,
			agent_id      TEXT NOT NULL DEFAULT '',
			ticket_key    TEXT NOT NULL DEFAULT '',
			repo          TEXT NOT NULL DEFAULT '',
			branch        TEXT NOT NULL DEFAULT '',
			state         TEXT NOT NULL DEFAULT '',
			input_tokens  INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_create  INTEGER NOT NULL DEFAULT 0,
			cache_read    INTEGER NOT NULL DEFAULT 0,
			envelope      TEXT NOT NULL,
			timestamp     DATETIME NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_monarch_events_timestamp
			ON monarch_events (timestamp);

		CREATE INDEX IF NOT EXISTS idx_monarch_events_agent
			ON monarch_events (agent_id, timestamp);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return errors.WrapWithDetails(err, "create monarch schema")
	}
	return nil
}

// Insert persists a single monarch event. The full event is marshalled to JSON
// for lossless replay while the decomposed columns feed the indexed read paths.
func (s *Store) Insert(ctx context.Context, e Event) error {
	envelope, err := json.Marshal(e)
	if err != nil {
		return errors.WrapWithDetails(err, "marshal monarch envelope")
	}

	var in, out, cc, cr int
	if e.Payload != nil {
		in, out, cc, cr = e.Payload.InputTokens, e.Payload.OutputTokens, e.Payload.CacheCreate, e.Payload.CacheRead
	}

	ts := e.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO monarch_events (
			type, team, daemon_id, agent_id, ticket_key, repo, branch, state,
			input_tokens, output_tokens, cache_create, cache_read, envelope, timestamp
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		string(e.Type), e.Team, e.DaemonID, e.AgentID, e.TicketKey, e.Repo, e.Branch, string(e.State),
		in, out, cc, cr, string(envelope), ts.UTC().Format(dbTimeFormat),
	)
	if err != nil {
		return errors.WrapWithDetails(err, "insert monarch event")
	}
	return nil
}

// WorkItem is one row of the work board: the latest known state of an agent
// held by an anonymous daemon.
type WorkItem struct {
	DaemonID  string
	AgentID   string
	TicketKey string
	Repo      string
	Branch    string
	State     string
	UpdatedAt time.Time
}

// WorkBoard returns the latest known state per (daemon_id, agent_id) whose most
// recent event is newer than since and is NOT agent.stop. Newest-first.
func (s *Store) WorkBoard(ctx context.Context, since time.Time) ([]WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT daemon_id, agent_id, ticket_key, repo, branch, state, timestamp
		FROM monarch_events
		WHERE id IN (
			SELECT MAX(id) FROM monarch_events
			WHERE timestamp >= ? AND type != ?
			GROUP BY daemon_id, agent_id
		)
		AND type != ?
		ORDER BY timestamp DESC
	`, since.UTC().Format(dbTimeFormat), string(EventHeartbeat), string(EventAgentStop))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query monarch work board")
	}
	defer func() { _ = rows.Close() }()

	var result []WorkItem
	for rows.Next() {
		var w WorkItem
		var ts string
		if err := rows.Scan(&w.DaemonID, &w.AgentID, &w.TicketKey, &w.Repo, &w.Branch, &w.State, &ts); err != nil {
			return nil, errors.WrapWithDetails(err, "scan monarch work item")
		}
		w.UpdatedAt, _ = time.Parse(dbTimeFormat, ts)
		result = append(result, w)
	}
	return result, rows.Err()
}

// BurnRow is one burn aggregation row keyed by ticket (or repo when the ticket
// is unknown).
type BurnRow struct {
	Key          string
	InputTokens  int
	OutputTokens int
	CacheCreate  int
	CacheRead    int
}

// burnByTicketQuery and burnByRepoQuery differ only in the key expression. They
// are full literal statements (not assembled from fragments) so the SQL is a
// compile-time constant — no string-built query can ever reach the driver.
const burnByTicketQuery = `
	SELECT CASE WHEN ticket_key != '' THEN ticket_key ELSE repo END AS k,
		SUM(input_tokens), SUM(output_tokens), SUM(cache_create), SUM(cache_read)
	FROM monarch_events
	WHERE id IN (
		SELECT MAX(id) FROM monarch_events
		WHERE type = ? AND timestamp >= ?
		GROUP BY agent_id
	)
	GROUP BY k
	ORDER BY k
`

const burnByRepoQuery = `
	SELECT repo AS k,
		SUM(input_tokens), SUM(output_tokens), SUM(cache_create), SUM(cache_read)
	FROM monarch_events
	WHERE id IN (
		SELECT MAX(id) FROM monarch_events
		WHERE type = ? AND timestamp >= ?
		GROUP BY agent_id
	)
	GROUP BY k
	ORDER BY k
`

// BurnByTicket sums the latest tokens.used row per agent (payload is cumulative
// per session) within the day, grouped by ticket_key. An empty ticket_key falls
// back to the repo so ad-hoc sessions still contribute a labelled row.
func (s *Store) BurnByTicket(ctx context.Context, dayStart time.Time) ([]BurnRow, error) {
	return s.runBurnQuery(ctx, burnByTicketQuery, dayStart)
}

// BurnByRepo sums the latest tokens.used row per agent within the day, grouped
// by repo.
func (s *Store) BurnByRepo(ctx context.Context, dayStart time.Time) ([]BurnRow, error) {
	return s.runBurnQuery(ctx, burnByRepoQuery, dayStart)
}

// runBurnQuery executes one of the constant burn queries and scans its rows,
// keeping the two public burn views in lockstep.
func (s *Store) runBurnQuery(ctx context.Context, query string, dayStart time.Time) ([]BurnRow, error) {
	rows, err := s.db.QueryContext(ctx, query, string(EventTokensUsed), dayStart.UTC().Format(dbTimeFormat))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query monarch burn")
	}
	defer func() { _ = rows.Close() }()

	var result []BurnRow
	for rows.Next() {
		var r BurnRow
		if err := rows.Scan(&r.Key, &r.InputTokens, &r.OutputTokens, &r.CacheCreate, &r.CacheRead); err != nil {
			return nil, errors.WrapWithDetails(err, "scan monarch burn row")
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// Capacity is the swarm headcount derived from each daemon's latest state.
type Capacity struct {
	Daemons int
	Busy    int
	Blocked int
	Idle    int
}

// Capacity counts distinct daemons within since, classifying each by its latest
// state: coding/planning -> busy, blocked -> blocked, anything else -> idle.
// A daemon whose latest event is a stop is excluded entirely.
func (s *Store) Capacity(ctx context.Context, since time.Time) (Capacity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT state, type
		FROM monarch_events
		WHERE id IN (
			SELECT MAX(id) FROM monarch_events
			WHERE timestamp >= ?
			GROUP BY daemon_id
		)
	`, since.UTC().Format(dbTimeFormat))
	if err != nil {
		return Capacity{}, errors.WrapWithDetails(err, "query monarch capacity")
	}
	defer func() { _ = rows.Close() }()

	var c Capacity
	for rows.Next() {
		var state, typ string
		if err := rows.Scan(&state, &typ); err != nil {
			return Capacity{}, errors.WrapWithDetails(err, "scan monarch capacity row")
		}
		classifyCapacity(&c, State(state), EventType(typ))
	}
	return c, rows.Err()
}

// classifyCapacity buckets one daemon's latest state into the capacity counters.
// Stopped daemons are intentionally not counted at all (neither in Daemons nor
// any bucket) so the console reflects live capacity only.
func classifyCapacity(c *Capacity, state State, typ EventType) {
	if typ == EventAgentStop || state == StateStopped {
		return
	}
	switch state {
	case StateCoding, StatePlanning:
		c.Busy++
	case StateBlocked:
		c.Blocked++
	default:
		c.Idle++
	}
	c.Daemons++
}

// Prune deletes events older than RetentionDays.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().Add(-RetentionDays * 24 * time.Hour).Format(dbTimeFormat)
	result, err := s.db.ExecContext(ctx, "DELETE FROM monarch_events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "prune monarch events")
	}
	return result.RowsAffected()
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
