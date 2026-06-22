package audit

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

// RetentionDays is the rolling window for audit event retention. Audit trails
// warrant longer retention than the stats trend window: accountability records
// of what an agent did and why are evidence that must outlive the short-lived
// trend graphs, so 90 days rather than 30.
const RetentionDays = 90

// dbTimeFormat is a fixed-width timestamp layout so lexical string comparison
// equals chronological ordering for range and prune filters.
const dbTimeFormat = "2006-01-02 15:04:05"

// Store persists audit events in SQLite for a structured, queryable trail.
// The full CloudEvents envelope is stored as JSON alongside decomposed,
// indexed columns so queries stay fast while the envelope round-trips intact.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite database at dbPath and ensures the
// schema is up to date. Use ":memory:" for in-memory databases in tests.
func NewStore(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create audit directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open audit database", "path", dbPath)
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
		CREATE TABLE IF NOT EXISTS audit_events (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id      TEXT NOT NULL,
			source        TEXT NOT NULL,
			type          TEXT NOT NULL,
			subject       TEXT NOT NULL DEFAULT '',
			operation     TEXT NOT NULL,
			tracker_kind  TEXT NOT NULL DEFAULT '',
			tracker_name  TEXT NOT NULL DEFAULT '',
			resource_key  TEXT NOT NULL DEFAULT '',
			outcome       TEXT NOT NULL,
			model_id      TEXT NOT NULL DEFAULT '',
			model_version TEXT NOT NULL DEFAULT '',
			rationale     TEXT NOT NULL DEFAULT '',
			envelope      TEXT NOT NULL,
			timestamp     DATETIME NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_audit_events_timestamp
			ON audit_events (timestamp);

		CREATE INDEX IF NOT EXISTS idx_audit_events_subject
			ON audit_events (resource_key, timestamp);

		CREATE INDEX IF NOT EXISTS idx_audit_events_tracker
			ON audit_events (tracker_kind, tracker_name, timestamp);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return errors.WrapWithDetails(err, "create audit schema")
	}
	return nil
}

// Insert persists a single audit event. The full envelope is marshalled to
// JSON for durable, lossless replay while the decomposed columns feed the
// indexed query paths.
func (s *Store) Insert(ctx context.Context, e Event) error {
	envelope, err := json.Marshal(e)
	if err != nil {
		return errors.WrapWithDetails(err, "marshal audit envelope")
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (
			event_id, source, type, subject, operation,
			tracker_kind, tracker_name, resource_key, outcome,
			model_id, model_version, rationale, envelope, timestamp
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, e.Source, e.Type, e.Subject, e.Data.Operation,
		e.Data.Actor.TrackerKind, e.Data.Actor.TrackerName, e.Data.Resource.Key, string(e.Data.Outcome),
		e.Data.Decision.ModelID, e.Data.Decision.ModelVersion, e.Data.Decision.Rationale,
		string(envelope), e.Time.UTC().Format(dbTimeFormat),
	)
	if err != nil {
		return errors.WrapWithDetails(err, "insert audit event")
	}
	return nil
}

// Filter narrows an audit query. A zero Limit defaults to 100.
type Filter struct {
	Since       time.Time
	Until       time.Time
	Subject     string
	TrackerKind string
	Limit       int
}

// Query returns matching audit events newest-first by reconstructing each
// stored envelope. Decomposed columns drive the WHERE clause so the indexes
// are used; the envelope JSON is the single source of truth for the result.
func (s *Store) Query(ctx context.Context, f Filter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	query := "SELECT envelope FROM audit_events WHERE timestamp >= ? AND timestamp <= ?"
	args := []any{
		f.Since.UTC().Format(dbTimeFormat),
		f.Until.UTC().Format(dbTimeFormat),
	}
	if f.Subject != "" {
		query += " AND resource_key = ?"
		args = append(args, f.Subject)
	}
	if f.TrackerKind != "" {
		query += " AND tracker_kind = ?"
		args = append(args, f.TrackerKind)
	}
	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query audit events")
	}
	defer func() { _ = rows.Close() }()

	var result []Event
	for rows.Next() {
		var envelope string
		if err := rows.Scan(&envelope); err != nil {
			return nil, errors.WrapWithDetails(err, "scan audit envelope")
		}
		var e Event
		if err := json.Unmarshal([]byte(envelope), &e); err != nil {
			return nil, errors.WrapWithDetails(err, "unmarshal audit envelope")
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// Prune deletes events older than RetentionDays.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().Add(-RetentionDays * 24 * time.Hour).Format(dbTimeFormat)
	result, err := s.db.ExecContext(ctx, "DELETE FROM audit_events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "prune audit events")
	}
	return result.RowsAffected()
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
