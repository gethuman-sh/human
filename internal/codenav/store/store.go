// Package store owns the SQLite database: connection, schema migration,
// project lifecycle, and a Writer that persists what indexers extract.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go, CGO-free SQLite driver (registers "sqlite")
)

//go:embed schema.sql
var schemaSQL string

// Store is a handle to the codenav database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path and applies the schema.
// PRAGMAs are passed in the DSN so every connection inherits them; a single
// open connection keeps WAL writes simple for a CLI.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Serialize access: simplest correct choice for a CLI and avoids
	// per-connection PRAGMA drift.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Migrate databases created before source_sig existed (ignore "duplicate column").
	_, _ = db.Exec(`ALTER TABLE project ADD COLUMN source_sig TEXT`)
	// Migrate databases created before incremental refresh: cheap change-detection
	// fingerprint on file, and route attribution to its registration file.
	_, _ = db.Exec(`ALTER TABLE file  ADD COLUMN mtime INTEGER`)
	_, _ = db.Exec(`ALTER TABLE file  ADD COLUMN size  INTEGER`)
	_, _ = db.Exec(`ALTER TABLE route ADD COLUMN file_id INTEGER`)
	return &Store{db: db, path: path}, nil
}

// heuristicFidelity is the fidelity string tree-sitter symbols carry; it mirrors
// index.Heuristic and is duplicated here to avoid a store->index import cycle.
const heuristicFidelity = "heuristic"

// FileMeta is the change-detection fingerprint stored for one indexed file.
type FileMeta struct {
	Hash  string
	Size  int64
	MTime int64
}

// ProjectExists reports whether a project row is present (an index exists).
func (s *Store) ProjectExists(name string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM project WHERE name=?`, name).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ProjectFiles returns path -> FileMeta for every recorded file of a project,
// the baseline the refresh diffs the working tree against. Pre-migration rows
// (NULL size/mtime -> 0) force a hash comparison, which is safe.
func (s *Store) ProjectFiles(name string) (map[string]FileMeta, error) {
	rows, err := s.db.Query(`
		SELECT f.path, COALESCE(f.content_hash,''), COALESCE(f.size,0), COALESCE(f.mtime,0)
		FROM file f JOIN project p ON p.id=f.project_id
		WHERE p.name=?`, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]FileMeta{}
	for rows.Next() {
		var path string
		var m FileMeta
		if err := rows.Scan(&path, &m.Hash, &m.Size, &m.MTime); err != nil {
			return nil, err
		}
		out[path] = m
	}
	return out, rows.Err()
}

// ProjectSig returns the stored source signature for a project, or "" if the
// project is not indexed.
func (s *Store) ProjectSig(name string) (string, error) {
	var sig sql.NullString
	err := s.db.QueryRow(`SELECT source_sig FROM project WHERE name=?`, name).Scan(&sig)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sig.String, nil
}

// SetProjectSig records the source signature after a successful index.
func (s *Store) SetProjectSig(name, sig string) error {
	_, err := s.db.Exec(`UPDATE project SET source_sig=? WHERE name=?`, sig, name)
	return err
}

// DB exposes the underlying handle for read-side packages (query, graph).
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ProjectInfo summarizes one indexed repo.
type ProjectInfo struct {
	Name      string
	Root      string
	VcsRev    string
	IndexedAt time.Time
	Symbols   int
	Edges     int
	Files     int
}

// ListProjects returns all indexed projects with counts.
func (s *Store) ListProjects() ([]ProjectInfo, error) {
	// Single query (no nested queries: the pool serializes to one connection).
	rows, err := s.db.Query(`
		SELECT p.name, p.root_path, COALESCE(p.vcs_rev,''), COALESCE(p.indexed_at,0),
		       (SELECT COUNT(*) FROM symbol WHERE project_id=p.id),
		       (SELECT COUNT(*) FROM file   WHERE project_id=p.id),
		       (SELECT COUNT(*) FROM edge e JOIN symbol s ON e.src_id=s.id WHERE s.project_id=p.id)
		FROM project p ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ProjectInfo
	for rows.Next() {
		var pi ProjectInfo
		var ts int64
		if err := rows.Scan(&pi.Name, &pi.Root, &pi.VcsRev, &ts, &pi.Symbols, &pi.Files, &pi.Edges); err != nil {
			return nil, err
		}
		if ts > 0 {
			pi.IndexedAt = time.Unix(ts, 0)
		}
		out = append(out, pi)
	}
	return out, rows.Err()
}

// DeleteProject removes a project and all its rows (incl. FTS, which is not
// covered by foreign-key cascades because the FTS tables are virtual).
func (s *Store) DeleteProject(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM fts_code   WHERE project=?`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM fts_symbol WHERE project=?`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM project WHERE name=?`, name); err != nil {
		return err
	}
	return tx.Commit()
}
