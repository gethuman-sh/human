package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	humanerrors "github.com/gethuman-sh/human/errors"
	_ "modernc.org/sqlite"
)

// DefaultIdeationDBPath returns the path to the ideation database
// (~/.human/ideation.db), creating the directory if needed.
func DefaultIdeationDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "ideation.db")
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, "ideation.db")
}

// IdeationDB persists the board's single ideation session in SQLite so a live
// chat survives a daemon restart — in particular the self-restart handover,
// which lands between turns precisely when the user is composing a reply.
//
// The engine owns exactly one session, so the table holds exactly one row,
// pinned by a CHECK-constrained singleton key rather than accumulating history.
type IdeationDB struct {
	db *sql.DB
}

// NewIdeationDB opens (or creates) the ideation database and ensures its
// schema. Use ":memory:" in tests.
func NewIdeationDB(dbPath string) (*IdeationDB, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, humanerrors.WrapWithDetails(err, "create ideation directory", "path", dir)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, humanerrors.WrapWithDetails(err, "open ideation database", "path", dbPath)
	}

	db.SetMaxOpenConns(1)

	// busy_timeout + WAL keep the brief two-process overlap of a self-restart
	// handover (old and new daemon both open) a retry rather than an error.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, humanerrors.WrapWithDetails(err, "set busy_timeout")
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, humanerrors.WrapWithDetails(err, "set WAL mode")
	}

	i := &IdeationDB{db: db}
	if err := i.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return i, nil
}

func (i *IdeationDB) ensureSchema() error {
	const schema = `
		CREATE TABLE IF NOT EXISTS ideation_session (
			singleton  INTEGER PRIMARY KEY CHECK (singleton = 1),
			session_id TEXT NOT NULL,
			updated_at DATETIME NOT NULL,
			data       TEXT NOT NULL
		);
	`
	if _, err := i.db.Exec(schema); err != nil {
		return humanerrors.WrapWithDetails(err, "create ideation schema")
	}
	return nil
}

// Save replaces the stored session. The whole session is one JSON document:
// it is read and written as a unit, so columns per field would buy nothing.
func (i *IdeationDB) Save(p PersistedIdeation) error {
	data, err := json.Marshal(p)
	if err != nil {
		return humanerrors.WrapWithDetails(err, "encode ideation session", "session", p.ID)
	}
	_, err = i.db.Exec(`
		INSERT INTO ideation_session (singleton, session_id, updated_at, data)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			session_id = excluded.session_id,
			updated_at = excluded.updated_at,
			data       = excluded.data
	`, p.ID, p.UpdatedAt, string(data))
	if err != nil {
		return humanerrors.WrapWithDetails(err, "save ideation session", "session", p.ID)
	}
	return nil
}

// Load returns the stored session, or (nil, nil) when nothing is stored.
func (i *IdeationDB) Load() (*PersistedIdeation, error) {
	var data string
	err := i.db.QueryRow(`SELECT data FROM ideation_session WHERE singleton = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, humanerrors.WrapWithDetails(err, "load ideation session")
	}
	var p PersistedIdeation
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, humanerrors.WrapWithDetails(err, "decode ideation session")
	}
	return &p, nil
}

// Clear removes the stored session.
func (i *IdeationDB) Clear() error {
	if _, err := i.db.Exec(`DELETE FROM ideation_session WHERE singleton = 1`); err != nil {
		return humanerrors.WrapWithDetails(err, "clear ideation session")
	}
	return nil
}

// Close releases the database handle.
func (i *IdeationDB) Close() error {
	return i.db.Close()
}
