package recall

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// ReviewFinding is one blocking finding of one machine review round, kept
// after the loop that produced it has closed. The agent state store that
// holds the round's report is latest-wins and pruned after two weeks, so
// nothing durable said what was found in which file across the rounds, and
// the count of findings per class — the number that says whether a change to
// a prompt or a gate did anything — could only be made by hand (SC-5278).
type ReviewFinding struct {
	Project string `json:"project"`
	Key     string `json:"key"`
	PR      int    `json:"pr"`
	Round   int    `json:"round"`
	// Head is the branch tip the reviewer read for this round.
	Head string `json:"head"`
	File string `json:"file"`
	Slug string `json:"slug"`
	// Class is the reviewer's category for the finding (dependents, tests,
	// correctness, …); empty when the reviewer named none.
	Class string `json:"class"`
	Text  string `json:"text"`
	// Disposition is the fixer's exit for the round the finding belongs to
	// (done, needs-input) and Note its one-line account, both empty until the
	// fixer reports.
	Disposition string    `json:"disposition,omitempty"`
	Note        string    `json:"note,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// FindingsRecorder is the write side of the findings record, kept apart from
// Store so the search index's fakes stay untouched by it.
type FindingsRecorder interface {
	// RecordReviewFindings upserts a round's findings; a re-drive of the same
	// round refreshes the row instead of duplicating it.
	RecordReviewFindings(ctx context.Context, findings []ReviewFinding) error
	// SetFindingDisposition attaches the fixer's exit and note to every
	// finding of the round it answered.
	SetFindingDisposition(ctx context.Context, project, key string, pr, round int, disposition, note string) error
}

// FindingsReader is the read side of the findings record. It is a separate
// interface from FindingsRecorder and from Store for the same reason those are
// separate: a caller that only asks questions should not be able to write, and
// the search index's fakes stay untouched by either.
type FindingsReader interface {
	// FindingsForFiles returns the findings recorded against any of files,
	// newest first. Each element of files must already be normalized the way
	// the record holds it (daemon.NormalizeFindingFile); this store matches
	// strings and does not know the reviewer's anchor format.
	FindingsForFiles(ctx context.Context, project string, files []string, limit int) ([]ReviewFinding, error)
}

const reviewFindingsCreate = `
	CREATE TABLE IF NOT EXISTS review_findings (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		project     TEXT NOT NULL DEFAULT '',
		key         TEXT NOT NULL,
		pr          INTEGER NOT NULL DEFAULT 0,
		round       INTEGER NOT NULL DEFAULT 0,
		head        TEXT NOT NULL DEFAULT '',
		file        TEXT NOT NULL,
		slug        TEXT NOT NULL,
		class       TEXT NOT NULL DEFAULT '',
		text        TEXT NOT NULL DEFAULT '',
		disposition TEXT NOT NULL DEFAULT '',
		note        TEXT NOT NULL DEFAULT '',
		recorded_at DATETIME NOT NULL DEFAULT (datetime('now')),
		UNIQUE (project, key, pr, round, file, slug)
	);
	CREATE INDEX IF NOT EXISTS idx_review_findings_file ON review_findings (project, file);
	CREATE INDEX IF NOT EXISTS idx_review_findings_class ON review_findings (project, class, recorded_at);`

// RecordReviewFindings implements FindingsRecorder.
func (s *SQLiteStore) RecordReviewFindings(ctx context.Context, findings []ReviewFinding) error {
	if len(findings) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.WrapWithDetails(err, "begin review findings write")
	}
	defer func() { _ = tx.Rollback() }()
	for _, f := range findings {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO review_findings (project, key, pr, round, head, file, slug, class, text)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project, key, pr, round, file, slug) DO UPDATE SET
				head = excluded.head, class = excluded.class, text = excluded.text`,
			f.Project, f.Key, f.PR, f.Round, f.Head, f.File, f.Slug, f.Class, f.Text)
		if err != nil {
			return errors.WrapWithDetails(err, "record review finding", "key", f.Key, "file", f.File, "slug", f.Slug)
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.WrapWithDetails(err, "commit review findings")
	}
	return nil
}

// SetFindingDisposition implements FindingsRecorder.
func (s *SQLiteStore) SetFindingDisposition(ctx context.Context, project, key string, pr, round int, disposition, note string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_findings SET disposition = ?, note = ? WHERE project = ? AND key = ? AND pr = ? AND round = ?`,
		disposition, note, project, key, pr, round)
	if err != nil {
		return errors.WrapWithDetails(err, "record finding disposition", "key", key, "round", round)
	}
	return nil
}

// FindingClassCounts counts the findings recorded since a moment, per class —
// the per-run number a campaign compares between two runs. A finding the
// reviewer left unclassified counts under "".
func (s *SQLiteStore) FindingClassCounts(ctx context.Context, project string, since time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT class, COUNT(*) FROM review_findings WHERE project = ? AND recorded_at >= ? GROUP BY class`,
		project, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "count review findings by class")
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var class string
		var n int
		if err := rows.Scan(&class, &n); err != nil {
			return nil, errors.WrapWithDetails(err, "scan finding class count")
		}
		counts[class] = n
	}
	return counts, rows.Err()
}

// FindingsForKey returns a ticket's recorded findings, oldest round first,
// with the disposition each round's fixer left.
func (s *SQLiteStore) FindingsForKey(ctx context.Context, project, key string) ([]ReviewFinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project, key, pr, round, head, file, slug, class, text, disposition, note, recorded_at
		FROM review_findings WHERE project = ? AND key = ? ORDER BY round, id`, project, key)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "read review findings", "key", key)
	}
	defer func() { _ = rows.Close() }()
	return scanReviewFindings(rows)
}

// FindingsForFiles implements FindingsReader.
//
// A row's `file` is whatever the reviewer anchored on: usually repo-relative,
// occasionally absolute. The exact match answers the first (and is what
// idx_review_findings_file serves); the `/`-anchored suffix answers the second
// without matching `xfoo.go` when asked about `foo.go`.
func (s *SQLiteStore) FindingsForFiles(ctx context.Context, project string, files []string, limit int) ([]ReviewFinding, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	clauses := make([]string, 0, len(files))
	args := []any{project}
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		clauses = append(clauses, `(file = ? OR file LIKE ? ESCAPE '\')`)
		args = append(args, f, "%/"+escapeLike(f))
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	args = append(args, limit)
	// #nosec G202 -- every clause is the same literal built above; the paths are bound
	query := `
		SELECT project, key, pr, round, head, file, slug, class, text, disposition, note, recorded_at
		FROM review_findings
		WHERE project = ? AND (` + strings.Join(clauses, " OR ") + `)
		ORDER BY recorded_at DESC, id DESC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "read review findings by file", "files", strings.Join(files, ","))
	}
	defer func() { _ = rows.Close() }()
	return scanReviewFindings(rows)
}

// escapeLike neutralises the LIKE metacharacters in a path so a literal `_`
// (common in Go filenames) cannot match an arbitrary character.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// scanReviewFindings reads the twelve-column projection both finding queries
// select, so the two cannot disagree about column order.
func scanReviewFindings(rows *sql.Rows) ([]ReviewFinding, error) {
	var out []ReviewFinding
	for rows.Next() {
		var f ReviewFinding
		var at sql.NullTime
		if err := rows.Scan(&f.Project, &f.Key, &f.PR, &f.Round, &f.Head, &f.File, &f.Slug, &f.Class, &f.Text, &f.Disposition, &f.Note, &at); err != nil {
			return nil, errors.WrapWithDetails(err, "scan review finding")
		}
		if at.Valid {
			f.RecordedAt = at.Time
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
