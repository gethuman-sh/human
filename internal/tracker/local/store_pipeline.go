package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// The pipeline half of the store: indexed markers, the event log, the derived
// placement and classification columns, and the change counter. All of it is
// derived from writes the comment path already makes — the comment stays the
// truth, these are what make it queryable.

// ensurePipelineSchema adds the tables and columns the pipeline index needs.
// Columns are added one by one against the live table info so a database
// created by an earlier build migrates in place with no visible step.
func (s *store) ensurePipelineSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS markers (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue      INTEGER NOT NULL REFERENCES issues(number) ON DELETE CASCADE,
			comment_id INTEGER NOT NULL,
			type       TEXT NOT NULL,
			head       TEXT NOT NULL DEFAULT '',
			fields     TEXT NOT NULL DEFAULT '{}',
			author     TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS markers_issue ON markers(issue, id)`,
		`CREATE TABLE IF NOT EXISTS events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue      INTEGER NOT NULL,
			kind       TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			detail     TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS events_issue ON events(issue, id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return errors.WrapWithDetails(err, "creating local tracker pipeline schema")
		}
	}
	for _, col := range []struct{ name, def string }{
		{"kind", "TEXT NOT NULL DEFAULT ''"},
		{"maturity", "TEXT NOT NULL DEFAULT ''"},
		{"stage", "TEXT NOT NULL DEFAULT ''"},
		{"state", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn("issues", col.name, col.def); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) ensureColumn(table, name, def string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")") // #nosec G202 -- table is a literal from this file
	if err != nil {
		return errors.WrapWithDetails(err, "reading local tracker table info", "table", table)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk); err != nil {
			return errors.WrapWithDetails(err, "scanning local tracker table info", "table", table)
		}
		if colName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return errors.WrapWithDetails(err, "reading local tracker table info", "table", table)
	}
	if _, err := s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + def); err != nil { // #nosec G202 -- all three parts are literals from this file
		return errors.WrapWithDetails(err, "adding local tracker column", "table", table, "column", name)
	}
	return nil
}

// event kinds, as the history records them.
const (
	eventCreated   = "created"
	eventEdited    = "edited"
	eventStatus    = "status"
	eventAssigned  = "assigned"
	eventComment   = "comment"
	eventMarker    = "marker"
	eventLinked    = "linked"
	eventUnlinked  = "unlinked"
	eventDeleted   = "deleted"
	eventPlacement = "placement"
)

// record appends one event. Every write path calls it, which is what makes
// the event id a change counter for Version().
func (s *store) record(ctx context.Context, number int64, kind, actor, detail string) error {
	now := s.now().UTC().Format(timeLayout)
	if _, err := s.db.ExecContext(ctx, "INSERT INTO events (issue, kind, actor, detail, created_at) VALUES (?, ?, ?, ?, ?)",
		number, kind, actor, detail, now); err != nil {
		return errors.WrapWithDetails(err, "recording local tracker event", "number", number, "kind", kind)
	}
	return nil
}

type eventRow struct {
	ID        int64
	Issue     int64
	Kind      string
	Actor     string
	Detail    string
	CreatedAt time.Time
}

func (s *store) listEvents(ctx context.Context, number int64) ([]eventRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, issue, kind, actor, detail, created_at FROM events WHERE issue = ? ORDER BY id ASC", number)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing local tracker events", "number", number)
	}
	defer func() { _ = rows.Close() }()
	var out []eventRow
	for rows.Next() {
		var e eventRow
		var created string
		if err := rows.Scan(&e.ID, &e.Issue, &e.Kind, &e.Actor, &e.Detail, &created); err != nil {
			return nil, errors.WrapWithDetails(err, "scanning local tracker event", "number", number)
		}
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "reading local tracker events", "number", number)
	}
	return out, nil
}

// version is the highest event id: one integer that moves on every write.
func (s *store) version(ctx context.Context) (string, error) {
	var max sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(id) FROM events").Scan(&max); err != nil {
		return "", errors.WrapWithDetails(err, "reading local tracker version")
	}
	return strconv.FormatInt(max.Int64, 10), nil
}

type markerRow struct {
	ID        int64
	CommentID int64
	Type      string
	Head      string
	Fields    map[string]string
	Author    string
	CreatedAt time.Time
}

func (s *store) insertMarker(ctx context.Context, number, commentID int64, typ, head string, fields map[string]string, author string, at time.Time) error {
	if fields == nil {
		fields = map[string]string{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return errors.WrapWithDetails(err, "encoding local marker fields", "number", number, "type", typ)
	}
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO markers (issue, comment_id, type, head, fields, author, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		number, commentID, typ, head, string(raw), author, at.UTC().Format(timeLayout)); err != nil {
		return errors.WrapWithDetails(err, "indexing local marker", "number", number, "type", typ)
	}
	return nil
}

func (s *store) listMarkers(ctx context.Context, number int64) ([]markerRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, comment_id, type, head, fields, author, created_at FROM markers WHERE issue = ? ORDER BY id ASC", number)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing local markers", "number", number)
	}
	defer func() { _ = rows.Close() }()
	var out []markerRow
	for rows.Next() {
		var m markerRow
		var fields, created string
		if err := rows.Scan(&m.ID, &m.CommentID, &m.Type, &m.Head, &fields, &m.Author, &created); err != nil {
			return nil, errors.WrapWithDetails(err, "scanning local marker", "number", number)
		}
		if err := json.Unmarshal([]byte(fields), &m.Fields); err != nil {
			return nil, errors.WrapWithDetails(err, "decoding local marker fields", "number", number, "id", m.ID)
		}
		m.CreatedAt = parseTime(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "reading local markers", "number", number)
	}
	return out, nil
}

// markerTypes is the type-only history the FSM replays.
func (s *store) markerTypes(ctx context.Context, number int64) ([]string, error) {
	rows, err := s.listMarkers(ctx, number)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Type
	}
	return out, nil
}

// setFacets writes the derived classification and placement columns without
// touching updated_at: they restate what the daemon derived, they are not a
// change to the ticket a board poll should wake up for.
func (s *store) setFacets(ctx context.Context, number int64, kind, maturity string) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET kind = ?, maturity = ? WHERE number = ?", kind, maturity, number); err != nil {
		return errors.WrapWithDetails(err, "updating local issue classification", "number", number)
	}
	return nil
}

func (s *store) setPlacement(ctx context.Context, number int64, stage, state string) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET stage = ?, state = ? WHERE number = ?", stage, state, number); err != nil {
		return errors.WrapWithDetails(err, "updating local issue placement", "number", number)
	}
	return nil
}

// facets reads the derived columns for one issue.
func (s *store) facets(ctx context.Context, number int64) (kind, maturity, stage, state string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT kind, maturity, stage, state FROM issues WHERE number = ?", number).Scan(&kind, &maturity, &stage, &state)
	if err != nil {
		return "", "", "", "", errors.WrapWithDetails(err, "reading local issue facets", "number", number)
	}
	return kind, maturity, stage, state, nil
}

// facetsFor reads the derived columns for a listing in one query.
func (s *store) facetsFor(ctx context.Context, numbers []int64) (map[int64][4]string, error) {
	out := make(map[int64][4]string, len(numbers))
	if len(numbers) == 0 {
		return out, nil
	}
	marks := make([]string, len(numbers))
	args := make([]any, len(numbers))
	for i, n := range numbers {
		marks[i] = "?"
		args[i] = n
	}
	rows, err := s.db.QueryContext(ctx, "SELECT number, kind, maturity, stage, state FROM issues WHERE number IN ("+strings.Join(marks, ",")+")", args...) // #nosec G202 -- placeholders only; the numbers are bound
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading local issue facets")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n int64
		var f [4]string
		if err := rows.Scan(&n, &f[0], &f[1], &f[2], &f[3]); err != nil {
			return nil, errors.WrapWithDetails(err, "scanning local issue facets")
		}
		out[n] = f
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "reading local issue facets")
	}
	return out, nil
}
