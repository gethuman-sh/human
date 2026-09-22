// Package local is an issue tracker that lives in a SQLite file on this
// machine. It exists so a project can run the whole pipeline — ideas, plans,
// markers, handoffs, reviews — with no tracker account, no token and no network:
// the board is the only UI it has, and the CLI is the only way to write to it.
package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, same as the daemon's other stores

	"github.com/gethuman-sh/human/errors"
)

// timeLayout keeps timestamps sortable as text, which is how SQLite compares
// them in the UpdatedSince filter.
const timeLayout = time.RFC3339Nano

// row is one issue as stored, before it is shaped into a tracker.Issue.
type row struct {
	Number      int64
	Title       string
	Description string
	Type        string
	Status      string
	Priority    string
	Assignee    string
	Reporter    string
	Parent      int64
	Labels      []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// link is one stored relationship. For "blocks" Src blocks Dst; for "related"
// the pair is stored once with Src < Dst so the same link cannot exist twice.
type link struct {
	Src  int64
	Dst  int64
	Kind string
}

// store is the SQLite layer. It knows numbers and rows, never keys or
// tracker types, so the key grammar stays in one place (client.go).
type store struct {
	db  *sql.DB
	now func() time.Time
}

// openStore opens or creates the database and ensures the schema. ":memory:"
// gives tests a fresh, private database.
func openStore(path string, now func() time.Time) (*store, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.WrapWithDetails(err, "creating local tracker directory", "path", dir)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "opening local tracker database", "path", path)
	}
	// One connection: the daemon's goroutines and a hand-run CLI may both hold
	// the file open, and serialising writes through a single connection plus
	// the busy timeout is what keeps that safe without a lock file.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, errors.WrapWithDetails(err, "applying pragma", "pragma", pragma, "path", path)
		}
	}
	s := &store{db: db, now: now}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) ensureSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS issues (
			number      INTEGER PRIMARY KEY AUTOINCREMENT,
			title       TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			type        TEXT NOT NULL DEFAULT 'Task',
			status      TEXT NOT NULL DEFAULT 'Backlog',
			priority    TEXT NOT NULL DEFAULT '',
			assignee    TEXT NOT NULL DEFAULT '',
			reporter    TEXT NOT NULL DEFAULT '',
			parent      INTEGER NOT NULL DEFAULT 0,
			labels      TEXT NOT NULL DEFAULT '[]',
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue      INTEGER NOT NULL REFERENCES issues(number) ON DELETE CASCADE,
			author     TEXT NOT NULL,
			body       TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS links (
			src  INTEGER NOT NULL REFERENCES issues(number) ON DELETE CASCADE,
			dst  INTEGER NOT NULL REFERENCES issues(number) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			PRIMARY KEY (src, dst, kind)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return errors.WrapWithDetails(err, "creating local tracker schema")
		}
	}
	return nil
}

func (s *store) close() error { return s.db.Close() }

// listFilter is what listRows selects on. Zero values mean "no constraint".
type listFilter struct {
	Statuses     []string
	UpdatedSince time.Time
	Limit        int // 0 means all
}

const rowColumns = `number, title, description, type, status, priority, assignee, reporter, parent, labels, created_at, updated_at`

// listRows returns issues newest first. When Limit is set it fetches one row
// beyond it so the caller can report truncation without a second count query.
func (s *store) listRows(ctx context.Context, f listFilter) ([]row, bool, error) {
	var where []string
	var args []any
	if len(f.Statuses) > 0 {
		marks := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			marks[i] = "?"
			args = append(args, st)
		}
		where = append(where, "status IN ("+strings.Join(marks, ",")+")")
	}
	if !f.UpdatedSince.IsZero() {
		where = append(where, "updated_at > ?")
		args = append(args, f.UpdatedSince.UTC().Format(timeLayout))
	}
	q := "SELECT " + rowColumns + " FROM issues"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- the joined parts are fixed column tests with ? placeholders; every value is bound
	}
	q += " ORDER BY number DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit+1)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, errors.WrapWithDetails(err, "listing local issues")
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, errors.WrapWithDetails(err, "reading local issues")
	}
	truncated := f.Limit > 0 && len(out) > f.Limit
	if truncated {
		out = out[:f.Limit]
	}
	return out, truncated, nil
}

func (s *store) getRow(ctx context.Context, number int64) (*row, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+rowColumns+" FROM issues WHERE number = ?", number)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading local issue", "number", number)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, nil
	}
	r, err := scanRow(rows)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(sc scanner) (row, error) {
	var r row
	var labels, created, updated string
	if err := sc.Scan(&r.Number, &r.Title, &r.Description, &r.Type, &r.Status, &r.Priority,
		&r.Assignee, &r.Reporter, &r.Parent, &labels, &created, &updated); err != nil {
		return row{}, errors.WrapWithDetails(err, "scanning local issue")
	}
	if err := json.Unmarshal([]byte(labels), &r.Labels); err != nil {
		return row{}, errors.WrapWithDetails(err, "decoding local issue labels", "number", r.Number)
	}
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	return r, nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *store) insertRow(ctx context.Context, r row) (int64, error) {
	labels, err := json.Marshal(nonNil(r.Labels))
	if err != nil {
		return 0, errors.WrapWithDetails(err, "encoding local issue labels")
	}
	now := s.now().UTC().Format(timeLayout)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO issues (title, description, type, status, priority, assignee, reporter, parent, labels, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Title, r.Description, r.Type, r.Status, r.Priority, r.Assignee, r.Reporter, r.Parent, string(labels), now, now)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "creating local issue", "title", r.Title)
	}
	number, err := res.LastInsertId()
	if err != nil {
		return 0, errors.WrapWithDetails(err, "reading new local issue number")
	}
	return number, nil
}

// updateRow writes every mutable column back; callers edit the row they read.
func (s *store) updateRow(ctx context.Context, r row) error {
	labels, err := json.Marshal(nonNil(r.Labels))
	if err != nil {
		return errors.WrapWithDetails(err, "encoding local issue labels", "number", r.Number)
	}
	now := s.now().UTC().Format(timeLayout)
	_, err = s.db.ExecContext(ctx,
		`UPDATE issues SET title = ?, description = ?, type = ?, status = ?, priority = ?, assignee = ?, parent = ?, labels = ?, updated_at = ?
		 WHERE number = ?`,
		r.Title, r.Description, r.Type, r.Status, r.Priority, r.Assignee, r.Parent, string(labels), now, r.Number)
	if err != nil {
		return errors.WrapWithDetails(err, "updating local issue", "number", r.Number)
	}
	return nil
}

func (s *store) deleteRow(ctx context.Context, number int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM issues WHERE number = ?", number); err != nil {
		return errors.WrapWithDetails(err, "deleting local issue", "number", number)
	}
	return nil
}

// touch advances updated_at without changing content, so a comment or a link
// moves the issue in UpdatedSince listings the way it does on every backend.
func (s *store) touch(ctx context.Context, number int64) error {
	now := s.now().UTC().Format(timeLayout)
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET updated_at = ? WHERE number = ?", now, number); err != nil {
		return errors.WrapWithDetails(err, "touching local issue", "number", number)
	}
	return nil
}

type commentRow struct {
	ID        int64
	Author    string
	Body      string
	CreatedAt time.Time
}

func (s *store) listComments(ctx context.Context, number int64) ([]commentRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, author, body, created_at FROM comments WHERE issue = ? ORDER BY id ASC", number)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing local comments", "number", number)
	}
	defer func() { _ = rows.Close() }()
	var out []commentRow
	for rows.Next() {
		var c commentRow
		var created string
		if err := rows.Scan(&c.ID, &c.Author, &c.Body, &created); err != nil {
			return nil, errors.WrapWithDetails(err, "scanning local comment", "number", number)
		}
		c.CreatedAt = parseTime(created)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "reading local comments", "number", number)
	}
	return out, nil
}

func (s *store) insertComment(ctx context.Context, number int64, author, body string) (commentRow, error) {
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, "INSERT INTO comments (issue, author, body, created_at) VALUES (?, ?, ?, ?)",
		number, author, body, now.Format(timeLayout))
	if err != nil {
		return commentRow{}, errors.WrapWithDetails(err, "adding local comment", "number", number)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return commentRow{}, errors.WrapWithDetails(err, "reading new local comment id", "number", number)
	}
	return commentRow{ID: id, Author: author, Body: body, CreatedAt: now}, nil
}

func (s *store) insertLink(ctx context.Context, l link) error {
	if _, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO links (src, dst, kind) VALUES (?, ?, ?)", l.Src, l.Dst, l.Kind); err != nil {
		return errors.WrapWithDetails(err, "linking local issues", "src", l.Src, "dst", l.Dst, "kind", l.Kind)
	}
	return nil
}

// deleteLinks removes every relationship between the pair, whichever way it
// was stored; unlink names two issues, not a direction.
func (s *store) deleteLinks(ctx context.Context, a, b int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM links WHERE (src = ? AND dst = ?) OR (src = ? AND dst = ?)", a, b, b, a); err != nil {
		return errors.WrapWithDetails(err, "unlinking local issues", "a", a, "b", b)
	}
	return nil
}

// linksTouching returns every link in which any of the numbers takes part.
// One query for a whole listing rather than one per issue.
func (s *store) linksTouching(ctx context.Context, numbers []int64) ([]link, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	marks := make([]string, len(numbers))
	args := make([]any, 0, 2*len(numbers))
	for i, n := range numbers {
		marks[i] = "?"
		args = append(args, n)
	}
	in := strings.Join(marks, ",")
	args = append(args, args...)
	rows, err := s.db.QueryContext(ctx, "SELECT src, dst, kind FROM links WHERE src IN ("+in+") OR dst IN ("+in+")", args...) // #nosec G202 -- in is a list of ? placeholders; the numbers are bound
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing local links")
	}
	defer func() { _ = rows.Close() }()
	var out []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.Src, &l.Dst, &l.Kind); err != nil {
			return nil, errors.WrapWithDetails(err, "scanning local link")
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "reading local links")
	}
	return out, nil
}

func nonNil(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}
