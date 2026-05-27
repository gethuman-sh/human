package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// SQLiteStore implements Store using SQLite with FTS5 full-text search.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and ensures
// the schema is up to date. Use ":memory:" for in-memory databases in tests.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create index directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open index database", "path", dbPath)
	}

	// SQLite serialises writers; cap connections to one writer at a time
	// so callers experience clean queueing instead of "database is locked"
	// errors when multiple goroutines hit the index concurrently.
	db.SetMaxOpenConns(1)

	// Wait up to 5 seconds for the writer lock before giving up.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, errors.WrapWithDetails(err, "set busy_timeout")
	}

	// WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, errors.WrapWithDetails(err, "set WAL mode")
	}

	s := &SQLiteStore{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) ensureSchema() error {
	const schema = `
		CREATE TABLE IF NOT EXISTS entries (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			key        TEXT NOT NULL,
			source     TEXT NOT NULL,
			kind       TEXT NOT NULL,
			project    TEXT NOT NULL DEFAULT '',
			title      TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT '',
			assignee   TEXT NOT NULL DEFAULT '',
			url        TEXT NOT NULL DEFAULT '',
			indexed_at DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE (key, source)
		);

		CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
			key,
			title,
			description,
			content='',
			contentless_delete=1,
			tokenize='porter unicode61'
		);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return errors.WrapWithDetails(err, "create index schema")
	}
	return nil
}

// UpsertEntry inserts or updates an entry and its FTS index.
func (s *SQLiteStore) UpsertEntry(ctx context.Context, entry Entry, description string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.WrapWithDetails(err, "begin transaction")
	}
	defer tx.Rollback() //nolint:errcheck

	// Check if entry exists.
	var existingID int64
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM entries WHERE key = ? AND source = ?",
		entry.Key, entry.Source,
	).Scan(&existingID)

	switch err {
	case nil:
		// Exists — delete old FTS row, update entries row.
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM entries_fts WHERE rowid = ?", existingID,
		); err != nil {
			return errors.WrapWithDetails(err, "delete old FTS entry", "key", entry.Key)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entries SET kind=?, project=?, title=?, status=?, assignee=?, url=?, indexed_at=datetime('now')
			 WHERE id = ?`,
			entry.Kind, entry.Project, entry.Title, entry.Status, entry.Assignee, entry.URL, existingID,
		); err != nil {
			return errors.WrapWithDetails(err, "update entry", "key", entry.Key)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO entries_fts(rowid, key, title, description) VALUES (?, ?, ?, ?)",
			existingID, entry.Key, entry.Title, description,
		); err != nil {
			return errors.WrapWithDetails(err, "insert FTS entry", "key", entry.Key)
		}
	case sql.ErrNoRows:
		// New entry.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO entries (key, source, kind, project, title, status, assignee, url)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.Key, entry.Source, entry.Kind, entry.Project, entry.Title, entry.Status, entry.Assignee, entry.URL,
		)
		if err != nil {
			return errors.WrapWithDetails(err, "insert entry", "key", entry.Key)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return errors.WrapWithDetails(err, "getting last insert ID", "key", entry.Key)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO entries_fts(rowid, key, title, description) VALUES (?, ?, ?, ?)",
			newID, entry.Key, entry.Title, description,
		); err != nil {
			return errors.WrapWithDetails(err, "insert FTS entry", "key", entry.Key)
		}
	default:
		return errors.WrapWithDetails(err, "check existing entry", "key", entry.Key)
	}

	return tx.Commit()
}

// DeleteEntry removes an entry and its FTS index.
func (s *SQLiteStore) DeleteEntry(ctx context.Context, key, source string) error {
	var id int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id FROM entries WHERE key = ? AND source = ?", key, source,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil // nothing to delete
	}
	if err != nil {
		return errors.WrapWithDetails(err, "find entry to delete", "key", key)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.WrapWithDetails(err, "begin transaction")
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, "DELETE FROM entries_fts WHERE rowid = ?", id); err != nil {
		return errors.WrapWithDetails(err, "delete FTS entry", "key", key)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM entries WHERE id = ?", id); err != nil {
		return errors.WrapWithDetails(err, "delete entry", "key", key)
	}
	return tx.Commit()
}

// Search performs a full-text search and returns matching entries ranked by BM25.
func (s *SQLiteStore) Search(ctx context.Context, query string, limit int) ([]Entry, error) {
	return s.SearchWithKind(ctx, query, "", limit)
}

// SearchWithKind performs a full-text search filtered by entries.kind.
// When kind is empty the query behaves exactly like Search. The kind
// filter is applied inside the SQL engine so the limit cannot exclude
// all matching kind rows when the top-ranked hits are of another kind.
func (s *SQLiteStore) SearchWithKind(ctx context.Context, query, kind string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 20
	}

	// Quote each word so FTS5 special characters (hyphens, colons) are
	// treated as literals rather than operators.
	ftsQuery := sanitizeFTSQuery(query)

	var rows *sql.Rows
	var err error
	if kind == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT e.key, e.source, e.kind, e.project, e.title, e.status, e.assignee, e.url, e.indexed_at
			FROM entries_fts f
			JOIN entries e ON e.id = f.rowid
			WHERE entries_fts MATCH ?
			ORDER BY bm25(entries_fts)
			LIMIT ?
		`, ftsQuery, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT e.key, e.source, e.kind, e.project, e.title, e.status, e.assignee, e.url, e.indexed_at
			FROM entries_fts f
			JOIN entries e ON e.id = f.rowid
			WHERE entries_fts MATCH ? AND e.kind = ?
			ORDER BY bm25(entries_fts)
			LIMIT ?
		`, ftsQuery, kind, limit)
	}
	if err != nil {
		return nil, errors.WrapWithDetails(err, "search index", "query", query, "kind", kind)
	}
	defer func() { _ = rows.Close() }()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var indexedAt string
		if err := rows.Scan(&e.Key, &e.Source, &e.Kind, &e.Project, &e.Title, &e.Status, &e.Assignee, &e.URL, &indexedAt); err != nil {
			return nil, errors.WrapWithDetails(err, "scan search result")
		}
		e.IndexedAt, _ = time.Parse("2006-01-02 15:04:05", indexedAt)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Stats returns statistics about the index.
func (s *SQLiteStore) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{
		ByKind:   make(map[string]int),
		BySource: make(map[string]int),
	}

	// Total count and last indexed.
	var lastIndexed sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*), MAX(indexed_at) FROM entries",
	).Scan(&st.TotalEntries, &lastIndexed)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query index stats")
	}
	if lastIndexed.Valid {
		st.LastIndexedAt, _ = time.Parse("2006-01-02 15:04:05", lastIndexed.String)
	}

	// By kind.
	rows, err := s.db.QueryContext(ctx, "SELECT kind, COUNT(*) FROM entries GROUP BY kind")
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query stats by kind")
	}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			_ = rows.Close()
			return nil, errors.WrapWithDetails(err, "scan kind stats")
		}
		st.ByKind[kind] = count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, errors.WrapWithDetails(err, "iterating kind stats rows")
	}
	_ = rows.Close()

	// By source.
	rows, err = s.db.QueryContext(ctx, "SELECT source, COUNT(*) FROM entries GROUP BY source")
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query stats by source")
	}
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			_ = rows.Close()
			return nil, errors.WrapWithDetails(err, "scan source stats")
		}
		st.BySource[source] = count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, errors.WrapWithDetails(err, "iterating source stats rows")
	}
	_ = rows.Close()

	return st, nil
}

// LastIndexedAt returns the most recent indexed_at timestamp for a given source.
// Returns the zero time if no entries exist for the source.
func (s *SQLiteStore) LastIndexedAt(ctx context.Context, source string) (time.Time, error) {
	var lastIndexed sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT MAX(indexed_at) FROM entries WHERE source = ?", source,
	).Scan(&lastIndexed)
	if err != nil {
		return time.Time{}, errors.WrapWithDetails(err, "query last indexed at", "source", source)
	}
	if !lastIndexed.Valid {
		return time.Time{}, nil
	}
	t, _ := time.Parse("2006-01-02 15:04:05", lastIndexed.String)
	return t, nil
}

// AllKeys returns all indexed keys for a given source instance.
func (s *SQLiteStore) AllKeys(ctx context.Context, source string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key FROM entries WHERE source = ?", source)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query all keys", "source", source)
	}
	defer func() { _ = rows.Close() }()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, errors.WrapWithDetails(err, "scan key")
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Ensure SQLiteStore implements Store at compile time.
var _ Store = (*SQLiteStore)(nil)

// sanitizeFTSQuery wraps each word in the query in double quotes so FTS5
// special characters (hyphens, colons, etc.) are treated as literals.
func sanitizeFTSQuery(query string) string {
	words := strings.Fields(query)
	for i, w := range words {
		// Strip existing quotes to avoid double-quoting.
		w = strings.Trim(w, `"`)
		// Escape internal double quotes by doubling them per FTS5 rules.
		w = strings.ReplaceAll(w, `"`, `""`)
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " ")
}
