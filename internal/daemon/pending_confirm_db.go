package daemon

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/gethuman-sh/human/errors"
	_ "modernc.org/sqlite"
)

// confirmDBTimeFormat is a fixed-width timestamp layout so lexical string
// comparison equals chronological ordering for the retention prune. Sub-second
// precision is lost across a restart — harmless against a 24h retention.
const confirmDBTimeFormat = "2006-01-02 15:04:05"

// confirmPersistence is the durability seam of PendingConfirmStore: the store
// stays the in-process source of truth and writes through to this sink, so
// tests can inject failures without SQLite and a nil sink means memory-only.
type confirmPersistence interface {
	Insert(pc PendingConfirmation) error
	UpdateResolved(pc PendingConfirmation) error
	Delete(id string) error
	DeleteOlderThan(cutoff time.Time) error
	LoadAll() ([]PendingConfirmation, error)
}

// DefaultConfirmDBPath returns the path to the confirmations database
// (~/.human/confirms.db), creating the directory if needed.
func DefaultConfirmDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "confirms.db")
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, "confirms.db")
}

// ConfirmDB persists permission requests in SQLite so approvals survive a
// daemon restart: a restarted daemon re-offers undecided prompts and honors
// unredeemed grants instead of silently dropping them.
type ConfirmDB struct {
	db *sql.DB
}

// NewConfirmDB opens (or creates) a SQLite database at dbPath and ensures the
// schema exists. Use ":memory:" for in-memory databases in tests.
func NewConfirmDB(dbPath string) (*ConfirmDB, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "create confirms directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "open confirms database", "path", dbPath)
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

	c := &ConfirmDB{db: db}
	if err := c.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return c, nil
}

func (c *ConfirmDB) ensureSchema() error {
	const schema = `
		CREATE TABLE IF NOT EXISTS pending_confirms (
			id          TEXT PRIMARY KEY,
			operation   TEXT NOT NULL,
			tracker     TEXT NOT NULL,
			issue_key   TEXT NOT NULL,
			prompt      TEXT NOT NULL DEFAULT '',
			client_pid  INTEGER NOT NULL DEFAULT 0,
			state       TEXT NOT NULL,
			created_at  DATETIME NOT NULL,
			resolved_at DATETIME
		);

		CREATE INDEX IF NOT EXISTS idx_pending_confirms_created_at
			ON pending_confirms (created_at);
	`
	if _, err := c.db.Exec(schema); err != nil {
		return errors.WrapWithDetails(err, "create confirms schema")
	}
	return nil
}

// Insert persists a new entry. INSERT OR IGNORE mirrors Submit's idempotency:
// a duplicate ID must never reset an already-persisted decision.
func (c *ConfirmDB) Insert(pc PendingConfirmation) error {
	_, err := c.db.Exec(`
		INSERT OR IGNORE INTO pending_confirms (
			id, operation, tracker, issue_key, prompt, client_pid, state, created_at, resolved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		pc.ID, pc.Operation, pc.Tracker, pc.Key, pc.Prompt, pc.ClientPID,
		string(pc.State), pc.CreatedAt.UTC().Format(confirmDBTimeFormat), resolvedAtColumn(pc.ResolvedAt),
	)
	if err != nil {
		return errors.WrapWithDetails(err, "insert confirmation", "id", pc.ID)
	}
	return nil
}

// UpdateResolved persists a decision (state + resolution time) for an entry.
func (c *ConfirmDB) UpdateResolved(pc PendingConfirmation) error {
	_, err := c.db.Exec(
		`UPDATE pending_confirms SET state = ?, resolved_at = ? WHERE id = ?`,
		string(pc.State), resolvedAtColumn(pc.ResolvedAt), pc.ID,
	)
	if err != nil {
		return errors.WrapWithDetails(err, "update confirmation", "id", pc.ID)
	}
	return nil
}

// Delete removes an entry (redeemed grant or explicit removal).
func (c *ConfirmDB) Delete(id string) error {
	if _, err := c.db.Exec(`DELETE FROM pending_confirms WHERE id = ?`, id); err != nil {
		return errors.WrapWithDetails(err, "delete confirmation", "id", id)
	}
	return nil
}

// DeleteOlderThan prunes entries created before cutoff, mirroring the store's
// retention sweep so a swept ID keeps reading as "unknown" after restarts.
func (c *ConfirmDB) DeleteOlderThan(cutoff time.Time) error {
	_, err := c.db.Exec(
		`DELETE FROM pending_confirms WHERE created_at < ?`,
		cutoff.UTC().Format(confirmDBTimeFormat),
	)
	if err != nil {
		return errors.WrapWithDetails(err, "prune confirmations")
	}
	return nil
}

// LoadAll returns every persisted entry, for absorbing into the in-memory
// store at daemon start.
func (c *ConfirmDB) LoadAll() ([]PendingConfirmation, error) {
	rows, err := c.db.Query(`
		SELECT id, operation, tracker, issue_key, prompt, client_pid, state, created_at, resolved_at
		FROM pending_confirms ORDER BY created_at
	`)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "load confirmations")
	}
	defer func() { _ = rows.Close() }()

	var out []PendingConfirmation
	for rows.Next() {
		var pc PendingConfirmation
		var state string
		// The driver converts DATETIME columns to time.Time on scan, so the
		// fixed-width strings written by Insert come back as times directly.
		var resolvedAt sql.NullTime
		if err := rows.Scan(&pc.ID, &pc.Operation, &pc.Tracker, &pc.Key, &pc.Prompt,
			&pc.ClientPID, &state, &pc.CreatedAt, &resolvedAt); err != nil {
			return nil, errors.WrapWithDetails(err, "scan confirmation row")
		}
		pc.State = ConfirmState(state)
		if resolvedAt.Valid {
			pc.ResolvedAt = resolvedAt.Time
		}
		out = append(out, pc)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "iterate confirmation rows")
	}
	return out, nil
}

// Close closes the underlying database.
func (c *ConfirmDB) Close() error {
	return c.db.Close()
}

// resolvedAtColumn maps the zero time to NULL so an unresolved entry
// round-trips as unresolved.
func resolvedAtColumn(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(confirmDBTimeFormat)
}
