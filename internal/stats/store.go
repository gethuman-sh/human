package stats

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/gethuman-sh/human/errors"
	_ "modernc.org/sqlite"
)

// RetentionDays is the rolling window for event retention.
const RetentionDays = 30

// StatsStore persists tool call events in SQLite for historical trend queries.
type StatsStore struct {
	db *sql.DB
}

// NewStatsStore opens (or creates) a SQLite database at dbPath and ensures
// the schema is up to date. Use ":memory:" for in-memory databases in tests.
func NewStatsStore(dbPath string) (*StatsStore, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create stats directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open stats database", "path", dbPath)
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

	s := &StatsStore{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *StatsStore) ensureSchema() error {
	const schema = `
		CREATE TABLE IF NOT EXISTS tool_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			event_name TEXT NOT NULL,
			tool_name  TEXT NOT NULL DEFAULT '',
			cwd        TEXT NOT NULL DEFAULT '',
			error_type TEXT,
			timestamp  DATETIME NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_tool_events_timestamp
			ON tool_events (timestamp);

		CREATE INDEX IF NOT EXISTS idx_tool_events_tool_name
			ON tool_events (tool_name, timestamp);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return errors.WrapWithDetails(err, "create stats schema")
	}
	return nil
}

// InsertEvent persists a single tool call event.
func (s *StatsStore) InsertEvent(ctx context.Context, sessionID, eventName, toolName, cwd, errorType string, ts time.Time) error {
	var errPtr *string
	if errorType != "" {
		errPtr = &errorType
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_events (session_id, event_name, tool_name, cwd, error_type, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`, sessionID, eventName, toolName, cwd, errPtr, ts.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return errors.WrapWithDetails(err, "insert tool event")
	}
	return nil
}

// Prune deletes events older than RetentionDays.
func (s *StatsStore) Prune(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().Add(-RetentionDays * 24 * time.Hour).Format("2006-01-02 15:04:05")
	result, err := s.db.ExecContext(ctx, "DELETE FROM tool_events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "prune tool events")
	}
	return result.RowsAffected()
}

// ToolCount holds a tool name and its event count.
type ToolCount struct {
	ToolName string `json:"tool_name"`
	Count    int    `json:"count"`
}

// TimeBucket holds a time bucket label and its event count.
type TimeBucket struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

// EventNameCount holds an event name and its count.
type EventNameCount struct {
	EventName string `json:"event_name"`
	Count     int    `json:"count"`
}

// ToolStats is the pre-aggregated stats payload sent to the TUI.
type ToolStats struct {
	ByTool      []ToolCount      `json:"by_tool"`
	ByHour      []TimeBucket     `json:"by_hour"`
	ByEventName []EventNameCount `json:"by_event_name"`
	TotalEvents int              `json:"total_events"`
	Since       time.Time        `json:"since"`
	Until       time.Time        `json:"until"`
}

// QueryByTool returns tool call counts grouped by tool_name for the given time range.
func (s *StatsStore) QueryByTool(ctx context.Context, since, until time.Time) ([]ToolCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tool_name, COUNT(*) as cnt
		FROM tool_events
		WHERE timestamp >= ? AND timestamp <= ? AND tool_name != ''
		GROUP BY tool_name
		ORDER BY cnt DESC
	`, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query by tool")
	}
	defer func() { _ = rows.Close() }()

	var result []ToolCount
	for rows.Next() {
		var tc ToolCount
		if err := rows.Scan(&tc.ToolName, &tc.Count); err != nil {
			return nil, errors.WrapWithDetails(err, "scan tool count")
		}
		result = append(result, tc)
	}
	return result, rows.Err()
}

// QueryByHour returns event counts grouped by hour for the given time range.
func (s *StatsStore) QueryByHour(ctx context.Context, since, until time.Time) ([]TimeBucket, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d %H:00', timestamp) as bucket, COUNT(*) as cnt
		FROM tool_events
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY bucket
		ORDER BY bucket ASC
	`, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query by hour")
	}
	defer func() { _ = rows.Close() }()

	var result []TimeBucket
	for rows.Next() {
		var tb TimeBucket
		if err := rows.Scan(&tb.Bucket, &tb.Count); err != nil {
			return nil, errors.WrapWithDetails(err, "scan time bucket")
		}
		result = append(result, tb)
	}
	return result, rows.Err()
}

// QueryByEventName returns event counts grouped by event_name for the given time range.
func (s *StatsStore) QueryByEventName(ctx context.Context, since, until time.Time) ([]EventNameCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_name, COUNT(*) as cnt
		FROM tool_events
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY event_name
		ORDER BY cnt DESC
	`, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query by event name")
	}
	defer func() { _ = rows.Close() }()

	var result []EventNameCount
	for rows.Next() {
		var enc EventNameCount
		if err := rows.Scan(&enc.EventName, &enc.Count); err != nil {
			return nil, errors.WrapWithDetails(err, "scan event name count")
		}
		result = append(result, enc)
	}
	return result, rows.Err()
}

// ToolOutcomeCounts is the ok/error split of tool calls in a range.
type ToolOutcomeCounts struct {
	OK    int `json:"ok"`
	Error int `json:"error"`
}

// QueryToolOutcomes returns the count of tool_events with a NULL error_type
// (ok) versus a non-NULL error_type (error) in [since, until]. InsertEvent
// writes error_type NULL when the error string is empty, so IS NULL is exactly
// the success predicate.
func (s *StatsStore) QueryToolOutcomes(ctx context.Context, since, until time.Time) (ToolOutcomeCounts, error) {
	var ok, errCount sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			SUM(CASE WHEN error_type IS NULL THEN 1 ELSE 0 END),
			SUM(CASE WHEN error_type IS NOT NULL THEN 1 ELSE 0 END)
		FROM tool_events
		WHERE timestamp >= ? AND timestamp <= ?
	`, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05")).Scan(&ok, &errCount)
	if err != nil {
		return ToolOutcomeCounts{}, errors.WrapWithDetails(err, "query tool outcomes")
	}
	// SUM over zero matching rows is NULL, not 0, so coalesce here.
	return ToolOutcomeCounts{OK: int(ok.Int64), Error: int(errCount.Int64)}, nil
}

// QueryTotal returns the total event count for the given time range.
func (s *StatsStore) QueryTotal(ctx context.Context, since, until time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tool_events WHERE timestamp >= ? AND timestamp <= ?
	`, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05")).Scan(&count)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "query total events")
	}
	return count, nil
}

// BuildToolStats runs all aggregation queries for a time range and returns a ready-to-render ToolStats.
func (s *StatsStore) BuildToolStats(ctx context.Context, since, until time.Time) (*ToolStats, error) {
	byTool, err := s.QueryByTool(ctx, since, until)
	if err != nil {
		return nil, err
	}
	byHour, err := s.QueryByHour(ctx, since, until)
	if err != nil {
		return nil, err
	}
	byEventName, err := s.QueryByEventName(ctx, since, until)
	if err != nil {
		return nil, err
	}
	total, err := s.QueryTotal(ctx, since, until)
	if err != nil {
		return nil, err
	}
	return &ToolStats{
		ByTool:      byTool,
		ByHour:      byHour,
		ByEventName: byEventName,
		TotalEvents: total,
		Since:       since,
		Until:       until,
	}, nil
}

// Close closes the database connection.
func (s *StatsStore) Close() error {
	return s.db.Close()
}
