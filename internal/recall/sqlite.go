package recall

import (
	"context"
	"database/sql"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// DefaultStaleAfter is how old the newest indexed entry may be before a search
// refuses to answer. It bounds the window in which "no results" could mean "the
// index stopped being updated" — long enough that a hand-run index is not
// constantly rejected, short enough that a broken sync surfaces the same day.
const DefaultStaleAfter = 24 * time.Hour

// ErrIndexEmpty means the index holds nothing, so a search cannot distinguish
// "no such ticket" from "nobody has indexed yet".
var ErrIndexEmpty = stderrors.New("search index is empty — nothing has been indexed yet; run `human index` or start the daemon")

// ErrIndexStale means the newest entry is older than StaleAfter, so the index
// may be missing everything recent.
var ErrIndexStale = stderrors.New("search index is stale — its newest entry is older than the freshness limit")

// SQLiteStore implements Store using SQLite with FTS5 full-text search.
type SQLiteStore struct {
	db *sql.DB
	// StaleAfter bounds how old the index may be before Search refuses. Zero
	// disables the check. Callers that genuinely want a possibly-stale answer
	// set it to zero deliberately, rather than getting one by accident.
	StaleAfter time.Duration
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

	s := &SQLiteStore{db: db, StaleAfter: DefaultStaleAfter}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// entriesCreate is shared between ensureSchema and migrateProjectUnique
// (which rebuilds entries under the same definition once the pre-SC-2326
// UNIQUE(key, source) no longer covers project).
const entriesCreate = `
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
		UNIQUE (key, source, project)
	)`

func (s *SQLiteStore) ensureSchema() error {
	const schema = entriesCreate + `;

		CREATE TABLE IF NOT EXISTS entry_files (
			key    TEXT NOT NULL,
			source TEXT NOT NULL,
			path   TEXT NOT NULL,
			PRIMARY KEY (key, source, path)
		);

		CREATE INDEX IF NOT EXISTS idx_entry_files_path ON entry_files (path);

		CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
			key,
			title,
			description,
			content='',
			contentless_delete=1,
			tokenize='porter unicode61'
		);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return errors.WrapWithDetails(err, "create index schema")
	}
	return s.migrateProjectUnique()
}

// migrateProjectUnique upgrades a database created before SC-2326, when
// entries carried a project column but the dedup identity was UNIQUE(key,
// source) alone: two projects sharing both a source and a key upserted onto
// one row, so one project's indexed issue silently replaced the other's in
// search results. It rebuilds entries under UNIQUE(key, source, project),
// preserving id so entries_fts rowids (which reference it) stay aligned — no
// re-index needed, and it is a no-op once the wider constraint is in place.
func (s *SQLiteStore) migrateProjectUnique() error {
	wide, err := s.hasProjectUniqueIndex()
	if err != nil {
		return err
	}
	if wide {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return errors.WrapWithDetails(err, "begin project-unique migration")
	}
	defer func() { _ = tx.Rollback() }()

	cols := "id, key, source, kind, project, title, status, assignee, url, indexed_at"
	stmts := []string{
		"ALTER TABLE entries RENAME TO entries_legacy",
		entriesCreate,
		"INSERT INTO entries (" + cols + ") SELECT " + cols + " FROM entries_legacy",
		"DROP TABLE entries_legacy",
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return errors.WrapWithDetails(err, "migrate entries to project-scoped unique index", "stmt", stmt)
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.WrapWithDetails(err, "commit project-unique migration")
	}
	return nil
}

// hasProjectUniqueIndex reports whether entries' unique index already covers
// project, by checking sqlite_master's stored CREATE TABLE text for the
// column — the guard that makes migrateProjectUnique idempotent.
func (s *SQLiteStore) hasProjectUniqueIndex() (bool, error) {
	var sqlText sql.NullString
	err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'entries'`,
	).Scan(&sqlText)
	if err != nil {
		if err == sql.ErrNoRows {
			return true, nil // no table yet — the CREATE above just made the current shape
		}
		return false, errors.WrapWithDetails(err, "read entries table definition")
	}
	return strings.Contains(sqlText.String, "UNIQUE (key, source, project)"), nil
}

// UpsertEntry inserts or updates an entry and its FTS index.
func (s *SQLiteStore) UpsertEntry(ctx context.Context, entry Entry, description string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.WrapWithDetails(err, "begin transaction")
	}
	defer tx.Rollback() //nolint:errcheck

	// Check if entry exists. project is part of the identity (SC-2326): two
	// projects that share both a source and a key are distinct entries, not
	// one row upserting over the other.
	var existingID int64
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM entries WHERE key = ? AND source = ? AND project = ?",
		entry.Key, entry.Source, entry.Project,
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

	// Replace this entry's path set wholesale: a re-planned ticket touches a
	// different set of files, and a stale path would report an overlap that no
	// longer exists.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM entry_files WHERE key = ? AND source = ?", entry.Key, entry.Source,
	); err != nil {
		return errors.WrapWithDetails(err, "clear entry files", "key", entry.Key)
	}
	for _, path := range entry.Files {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO entry_files (key, source, path) VALUES (?, ?, ?)",
			entry.Key, entry.Source, path,
		); err != nil {
			return errors.WrapWithDetails(err, "insert entry file", "key", entry.Key, "path", path)
		}
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
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM entry_files WHERE key = ? AND source = ?", key, source,
	); err != nil {
		return errors.WrapWithDetails(err, "delete entry files", "key", key)
	}
	return tx.Commit()
}

// Search performs a full-text search and returns matching entries ranked by BM25.
func (s *SQLiteStore) Search(ctx context.Context, query string, limit int) ([]Entry, error) {
	return s.SearchWithKind(ctx, query, "", limit)
}

// SearchByFile returns the entries whose plan names path.
//
// Exact match, deliberately. Asking "who else is changing this file" through
// full text does not work: the tokenizer splits internal/daemon/board_transition.go
// into "internal", "daemon", "board", "transition", "go" — words common enough
// to match much of the backlog — so the answer would be a ranking rather than a
// fact. This is the query that connects two tickets describing one problem in
// different words (SC-2132).
//
// It reports the index's usable state exactly as Search does: a lookup against
// an empty or stale record must not read as "nobody else is touching it".
func (s *SQLiteStore) SearchByFile(ctx context.Context, path string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 20
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if err := s.usable(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.key, e.source, e.kind, e.project, e.title, e.status, e.assignee, e.url, e.indexed_at
		FROM entry_files f
		JOIN entries e ON e.key = f.key AND e.source = f.source
		WHERE f.path = ?
		ORDER BY e.indexed_at DESC
		LIMIT ?
	`, path, limit)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "search index by file", "path", path)
	}
	defer func() { _ = rows.Close() }()
	return scanEntries(rows)
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
	// A blank or punctuation-only query sanitizes to "", which FTS5 rejects as
	// a syntax error; treat it as "no results" rather than surfacing raw SQL.
	// This is a fault in the question, not in the index, so it is checked first.
	if ftsQuery == "" {
		return nil, nil
	}
	// Refuse to answer from an index that cannot be trusted to hold the answer.
	// An empty or long-stale index returns "no results" for everything, which a
	// caller reads as "there is no such ticket" — and that is exactly how the
	// same work came to be done twice. "I could not look" must never render as
	// "there is nothing there" (SC-2132).
	if err := s.usable(ctx); err != nil {
		return nil, err
	}

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

	return scanEntries(rows)
}

// scanEntries reads the shared entry column list every search selects.
func scanEntries(rows *sql.Rows) ([]Entry, error) {
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
	// Joined with OR, not with a space. FTS5 reads adjacent terms as an implicit
	// AND, so a question phrased in a sentence required EVERY word to appear and
	// returned nothing — the caller then concluded there was no such ticket. Any
	// term may match and bm25 ranks the best overlap first, which is what makes
	// a natural-language question usable against a keyword index (SC-2132).
	return strings.Join(words, " OR ")
}

// usable reports whether the index can be trusted to answer at all: it must
// hold something, and that something must not be older than StaleAfter. The
// caller distinguishes these from an honest empty result.
func (s *SQLiteStore) usable(ctx context.Context) error {
	var count int
	var newest sql.NullString
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*), MAX(indexed_at) FROM entries",
	).Scan(&count, &newest); err != nil {
		return errors.WrapWithDetails(err, "checking index health")
	}
	if count == 0 {
		return ErrIndexEmpty
	}
	if s.StaleAfter <= 0 || !newest.Valid {
		return nil
	}
	indexedAt, err := time.Parse("2006-01-02 15:04:05", newest.String)
	if err != nil {
		// An unparseable timestamp is not evidence of freshness, but it is also
		// not evidence of staleness — do not block the search on it.
		return nil
	}
	if time.Since(indexedAt) > s.StaleAfter {
		return errors.WrapWithDetails(ErrIndexStale,
			"search index is stale — run `human index` or check the daemon's sync",
			"newest_entry", newest.String, "limit", s.StaleAfter.String())
	}
	return nil
}
