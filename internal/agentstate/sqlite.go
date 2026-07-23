package agentstate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"

	_ "modernc.org/sqlite" // pure-Go driver, same as the confirms store
)

// SQLiteStore persists agent state in SQLite. It follows the connection setup
// proven by the daemon's confirmations store: a single connection, WAL, and a
// busy timeout, so concurrent daemon goroutines and an out-of-daemon CLI can
// touch the same file without stepping on each other.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// Option customises a store at open time.
type Option func(*SQLiteStore)

// WithClock injects the time source. Lease expiry is time-driven, so tests
// drive it explicitly instead of sleeping.
func WithClock(now func() time.Time) Option {
	return func(s *SQLiteStore) {
		if now != nil {
			s.now = now
		}
	}
}

// Open opens (or creates) the database at dbPath and ensures the schema.
// Use ":memory:" in tests.
func Open(dbPath string, opts ...Option) (*SQLiteStore, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create state directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open state database", "path", dbPath)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, errors.WrapWithDetails(err, "apply pragma", "pragma", pragma)
		}
	}

	s := &SQLiteStore{db: db, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) ensureSchema() error {
	const schema = `
		CREATE TABLE IF NOT EXISTS agent_state (
			scope      TEXT NOT NULL,
			name       TEXT NOT NULL,
			value      TEXT NOT NULL,
			format     TEXT NOT NULL DEFAULT 'text',
			agent      TEXT NOT NULL DEFAULT '',
			run_id     TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			PRIMARY KEY (scope, name)
		);

		CREATE INDEX IF NOT EXISTS idx_agent_state_updated
			ON agent_state (updated_at);

		CREATE TABLE IF NOT EXISTS agent_leases (
			scope        TEXT NOT NULL,
			stage        TEXT NOT NULL,
			agent        TEXT NOT NULL,
			run_id       TEXT NOT NULL DEFAULT '',
			ttl_seconds  INTEGER NOT NULL DEFAULT 0,
			leased_at   TEXT NOT NULL,
			heartbeat_at TEXT NOT NULL,
			released_at  TEXT,
			PRIMARY KEY (scope, stage)
		);

		CREATE INDEX IF NOT EXISTS idx_agent_leases_heartbeat
			ON agent_leases (heartbeat_at);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return errors.WrapWithDetails(err, "create state schema")
	}
	return nil
}

// Close releases the database handle.
func (s *SQLiteStore) Close() error {
	if err := s.db.Close(); err != nil {
		return errors.WrapWithDetails(err, "close state database")
	}
	return nil
}

// Set writes (or overwrites) one entry and returns what was stored.
func (s *SQLiteStore) Set(ctx context.Context, scope, name, value, format string, meta Meta) (Entry, error) {
	e, err := s.buildEntry(scope, name, value, format, meta)
	if err != nil {
		return Entry{}, err
	}

	const q = `
		INSERT INTO agent_state (scope, name, value, format, agent, run_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, name) DO UPDATE SET
			value      = excluded.value,
			format     = excluded.format,
			agent      = excluded.agent,
			run_id     = excluded.run_id,
			updated_at = excluded.updated_at
	`
	_, err = s.db.ExecContext(ctx, q,
		e.Scope, e.Name, e.Value, e.Format, e.Agent, e.RunID, e.UpdatedAt.Format(TimeFormat))
	if err != nil {
		return Entry{}, errors.WrapWithDetails(err, "write state entry", "scope", e.Scope, "name", e.Name)
	}
	return e, nil
}

// buildEntry validates the inputs and stamps provenance, keeping Set itself
// free of validation branches.
func (s *SQLiteStore) buildEntry(scope, name, value, format string, meta Meta) (Entry, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return Entry{}, err
	}
	if err := ValidateName(name); err != nil {
		return Entry{}, err
	}
	if err := validateValue(value); err != nil {
		return Entry{}, err
	}
	normFormat, err := normalizeFormat(format, value)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Scope:     normScope,
		Name:      name,
		Value:     value,
		Format:    normFormat,
		Agent:     meta.Agent,
		RunID:     meta.RunID,
		UpdatedAt: s.now().UTC(),
	}, nil
}

// Get returns one entry, or ErrNotFound.
func (s *SQLiteStore) Get(ctx context.Context, scope, name string) (Entry, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return Entry{}, err
	}

	const q = `
		SELECT scope, name, value, format, agent, run_id, updated_at
		FROM agent_state WHERE scope = ? AND name = ?
	`
	row := s.db.QueryRowContext(ctx, q, normScope, name)
	e, err := scanEntry(row)
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

// List returns every entry in a scope, optionally restricted to a name prefix,
// ordered by name so output is stable across runs.
func (s *SQLiteStore) List(ctx context.Context, scope, prefix string) ([]Entry, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return nil, err
	}

	const q = `
		SELECT scope, name, value, format, agent, run_id, updated_at
		FROM agent_state
		WHERE scope = ? AND name LIKE ? ESCAPE '\'
		ORDER BY name
	`
	rows, err := s.db.QueryContext(ctx, q, normScope, escapeLike(prefix)+"%")
	if err != nil {
		return nil, errors.WrapWithDetails(err, "list state entries", "scope", normScope)
	}
	defer func() { _ = rows.Close() }()

	entries := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "read state entries", "scope", normScope)
	}
	return entries, nil
}

// Delete removes one entry, reporting whether it existed.
func (s *SQLiteStore) Delete(ctx context.Context, scope, name string) (bool, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_state WHERE scope = ? AND name = ?`, normScope, name)
	if err != nil {
		return false, errors.WrapWithDetails(err, "delete state entry", "scope", normScope, "name", name)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeletePrefix removes every entry of a scope whose name starts with prefix,
// returning how many went. It is how a run clears a namespace it owns — the
// retry budgets at the start of a fresh attempt — without touching the rest of
// the ticket's state. An empty prefix is refused rather than silently meaning
// "everything": that is what DeleteScope is for, and a typo should not wipe a
// ticket.
func (s *SQLiteStore) DeletePrefix(ctx context.Context, scope, prefix string) (int, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(prefix) == "" {
		return 0, errors.WithDetails("prefix must not be empty; use DeleteScope to clear a whole scope", "scope", normScope)
	}

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_state WHERE scope = ? AND name LIKE ? ESCAPE '\'`,
		normScope, escapeLike(prefix)+"%")
	if err != nil {
		return 0, errors.WrapWithDetails(err, "delete state prefix", "scope", normScope, "prefix", prefix)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteScope removes every entry and lease of a scope, returning the entry
// count. Used by `state rm --all` when a ticket's run is abandoned.
func (s *SQLiteStore) DeleteScope(ctx context.Context, scope string) (int, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_state WHERE scope = ?`, normScope)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "delete scope", "scope", normScope)
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agent_leases WHERE scope = ?`, normScope); err != nil {
		return 0, errors.WrapWithDetails(err, "delete scope leases", "scope", normScope)
	}
	return int(n), nil
}

// Incr adds to a counter entry and returns the new total, creating it at zero
// first. It runs in a transaction so two stages racing on a retry budget cannot
// both read the same value and overwrite each other.
func (s *SQLiteStore) Incr(ctx context.Context, scope, name string, by int64, meta Meta) (int64, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return 0, err
	}
	if err := ValidateName(name); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "begin counter transaction")
	}
	defer func() { _ = tx.Rollback() }()

	current, err := readCounter(ctx, tx, normScope, name)
	if err != nil {
		return 0, err
	}
	next := current + by

	const q = `
		INSERT INTO agent_state (scope, name, value, format, agent, run_id, updated_at)
		VALUES (?, ?, ?, 'text', ?, ?, ?)
		ON CONFLICT(scope, name) DO UPDATE SET
			value      = excluded.value,
			agent      = excluded.agent,
			run_id     = excluded.run_id,
			updated_at = excluded.updated_at
	`
	_, err = tx.ExecContext(ctx, q, normScope, name, strconv.FormatInt(next, 10),
		meta.Agent, meta.RunID, s.now().UTC().Format(TimeFormat))
	if err != nil {
		return 0, errors.WrapWithDetails(err, "write counter", "scope", normScope, "name", name)
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.WrapWithDetails(err, "commit counter transaction")
	}
	return next, nil
}

// readCounter returns the current counter value, treating a missing entry as
// zero and refusing a non-numeric one rather than silently resetting it.
func readCounter(ctx context.Context, tx *sql.Tx, scope, name string) (int64, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM agent_state WHERE scope = ? AND name = ?`,
		scope, name).Scan(&raw)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, errors.WrapWithDetails(err, "read counter", "scope", scope, "name", name)
	}
	n, convErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if convErr != nil {
		return 0, errors.WithDetails("existing value is not a counter", "scope", scope, "name", name, "value", raw)
	}
	return n, nil
}

// Prune removes entries not updated since cutoff and leases released before it,
// returning the number of entries dropped.
func (s *SQLiteStore) Prune(ctx context.Context, cutoff time.Time) (int, error) {
	stamp := cutoff.UTC().Format(TimeFormat)

	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_state WHERE updated_at < ?`, stamp)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "prune state entries")
	}
	n, _ := res.RowsAffected()

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_leases WHERE heartbeat_at < ?`, stamp); err != nil {
		return 0, errors.WrapWithDetails(err, "prune stale leases")
	}
	return int(n), nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows so one scan helper
// serves Get and List.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(sc rowScanner) (Entry, error) {
	var e Entry
	var stamp string
	if err := sc.Scan(&e.Scope, &e.Name, &e.Value, &e.Format, &e.Agent, &e.RunID, &stamp); err != nil {
		if isNoRows(err) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, errors.WrapWithDetails(err, "scan state entry")
	}
	ts, err := time.Parse(TimeFormat, stamp)
	if err != nil {
		return Entry{}, errors.WrapWithDetails(err, "parse state timestamp", "value", stamp)
	}
	e.UpdatedAt = ts
	return e, nil
}

// escapeLike neutralises the LIKE wildcards so a prefix such as "budget_fix"
// matches literally — names legitimately contain "_", which LIKE would
// otherwise treat as "any character".
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
