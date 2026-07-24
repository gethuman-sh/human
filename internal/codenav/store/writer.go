package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gethuman-sh/human/internal/codenav/index"
)

// Writer persists one indexing run for a single project inside a transaction.
// References, edges and routes are buffered and resolved at Commit, once every
// symbol's id is known (definitions may appear after their use sites).
//
// Writer implements index.Sink.
type Writer struct {
	s         *Store
	tx        *sql.Tx
	project   string
	root      string
	projectID int64

	fileIDs map[string]int64 // repo-relative path -> file.id
	symIDs  map[string]int64 // qname -> symbol.id

	refs   []index.Reference
	edges  []index.Edge
	routes []index.Route

	// Incremental refresh state (nil/false for a full rebuild). A reprocessed
	// file's rows are cleared once on first write; surviving symbol ids are
	// reused so references/edges from unchanged files keep resolving.
	incremental bool
	touched     map[string]bool             // files whose stale rows were cleared this run
	existing    map[string]map[string]int64 // file -> (qname -> surviving symbol id); entries left after reload are orphans
}

func scanID(tx *sql.Tx, query string, args ...any) (int64, error) {
	var id int64
	err := tx.QueryRow(query, args...).Scan(&id)
	return id, err
}

// NewWriter starts a fresh index of project at root. Any prior data for the
// project is removed first (M1 does full re-index).
func (s *Store) NewWriter(project, root string) (*Writer, error) {
	if err := s.DeleteProject(project); err != nil {
		return nil, fmt.Errorf("clear project: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	w := &Writer{
		s:       s,
		tx:      tx,
		project: project,
		root:    root,
		fileIDs: map[string]int64{},
		symIDs:  map[string]int64{},
	}
	id, err := scanID(tx, `INSERT INTO project(name, root_path) VALUES(?, ?) RETURNING id`, project, root)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("insert project: %w", err)
	}
	w.projectID = id
	return w, nil
}

// NewIncrementalWriter opens a refresh that preserves rows for untouched files:
// only reprocessed files are cleared and rewritten, and surviving symbol ids are
// reused so references/edges from unchanged files keep pointing at them. The
// project must already exist (error otherwise, so the caller can fall back to a
// full rebuild).
func (s *Store) NewIncrementalWriter(project, root string) (*Writer, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	id, err := scanID(tx, `SELECT id FROM project WHERE name=?`, project)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("project not indexed: %w", err)
	}
	return &Writer{
		s:           s,
		tx:          tx,
		project:     project,
		root:        root,
		projectID:   id,
		fileIDs:     map[string]int64{},
		symIDs:      map[string]int64{},
		incremental: true,
		touched:     map[string]bool{},
		existing:    map[string]map[string]int64{},
	}, nil
}

// ensureFile returns the file id for a repo-relative path, inserting a stub row
// if the path has not been seen yet.
func (w *Writer) ensureFile(path string) (int64, error) {
	if id, ok := w.fileIDs[path]; ok {
		return id, nil
	}
	id, err := scanID(w.tx,
		`INSERT INTO file(project_id, path, content_hash) VALUES(?, ?, '')
		 ON CONFLICT(project_id, path) DO UPDATE SET path=excluded.path
		 RETURNING id`, w.projectID, path)
	if err != nil {
		return 0, err
	}
	w.fileIDs[path] = id
	return id, nil
}

// File records file metadata and indexes the file body for code search. In an
// incremental refresh the file's stale rows are cleared and its symbols
// snapshotted the first time it is written this run, so reconciliation can reuse
// surviving symbol ids and drop the ones that disappeared.
func (w *Writer) File(f index.FileRec) error {
	if w.incremental && !w.touched[f.Path] {
		w.touched[f.Path] = true
		if err := w.clearReprocessed(f.Path); err != nil {
			return err
		}
	}
	id, err := w.ensureFile(f.Path)
	if err != nil {
		return err
	}
	var size, mtime int64
	if fi, e := os.Stat(filepath.Join(w.root, f.Path)); e == nil {
		size, mtime = fi.Size(), fi.ModTime().Unix()
	}
	if _, err := w.tx.Exec(
		`UPDATE file SET lang=?, content_hash=?, fidelity=?, size=?, mtime=? WHERE id=?`,
		f.Lang, f.ContentHash, string(f.Fidelity), size, mtime, id); err != nil {
		return err
	}
	if body, err := os.ReadFile(filepath.Join(w.root, f.Path)); err == nil {
		if _, err := w.tx.Exec(
			`INSERT INTO fts_code(body, path, project) VALUES(?, ?, ?)`,
			string(body), f.Path, w.project); err != nil {
			return err
		}
	}
	return nil
}

// clearReprocessed removes a reprocessed file's recomputed rows (fts_code by
// path, references and routes by file_id, edges whose source is one of the
// file's symbols) and snapshots its symbols (qname -> id) so Symbol can reuse
// surviving ids and deleteOrphans can drop the ones no longer emitted. The
// symbol rows themselves are kept until reconciliation to preserve stable ids.
func (w *Writer) clearReprocessed(path string) error {
	w.existing[path] = map[string]int64{}
	var fileID int64
	err := w.tx.QueryRow(`SELECT id FROM file WHERE project_id=? AND path=?`, w.projectID, path).Scan(&fileID)
	if err == sql.ErrNoRows {
		return nil // brand-new file in this refresh; nothing to clear
	}
	if err != nil {
		return err
	}
	// Snapshot surviving symbols so Symbol() can reuse ids in place.
	rows, err := w.tx.Query(`SELECT qname, id FROM symbol WHERE file_id=?`, fileID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var qn string
		var id int64
		if err := rows.Scan(&qn, &id); err != nil {
			_ = rows.Close()
			return err
		}
		w.existing[path][qn] = id
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Edges out of this file's symbols are recomputed from the reload.
	if _, err := w.tx.Exec(
		`DELETE FROM edge WHERE src_id IN (SELECT id FROM symbol WHERE file_id=?)`, fileID); err != nil {
		return err
	}
	// References and routes are file-scoped; FTS code rows are virtual (delete by path).
	if _, err := w.tx.Exec(`DELETE FROM reference WHERE file_id=?`, fileID); err != nil {
		return err
	}
	if _, err := w.tx.Exec(`DELETE FROM route WHERE file_id=?`, fileID); err != nil {
		return err
	}
	if _, err := w.tx.Exec(`DELETE FROM fts_code WHERE path=? AND project=?`, path, w.project); err != nil {
		return err
	}
	return nil
}

// Symbol inserts a definition and makes it searchable. In an incremental
// refresh a surviving qname's row is updated in place (keeping its stable id, so
// references/edges from unchanged files keep resolving); a genuinely new qname
// is inserted; qnames left unmatched at Commit are dropped by deleteOrphans.
func (w *Writer) Symbol(sym index.Symbol) error {
	fileID, err := w.ensureFile(sym.File)
	if err != nil {
		return err
	}
	if w.incremental {
		if id, ok := popExisting(w.existing[sym.File], sym.QName); ok {
			if _, err := w.tx.Exec(
				`UPDATE symbol SET file_id=?, name=?, kind=?, signature=?, doc=?,
				                   start_line=?, start_col=?, end_line=?, end_col=? WHERE id=?`,
				fileID, sym.Name, sym.Kind, sym.Signature, sym.Doc,
				sym.StartLine, sym.StartCol, sym.EndLine, sym.EndCol, id); err != nil {
				return err
			}
			w.symIDs[sym.QName] = id
			if _, err := w.tx.Exec(`DELETE FROM fts_symbol WHERE symbol_id=?`, id); err != nil {
				return err
			}
			_, err := w.tx.Exec(
				`INSERT INTO fts_symbol(name, qname, kind, symbol_id, project) VALUES(?,?,?,?,?)`,
				sym.Name, sym.QName, sym.Kind, id, w.project)
			return err
		}
	}
	id, err := scanID(w.tx,
		`INSERT INTO symbol(project_id, file_id, qname, name, kind, signature, doc,
		                    start_line, start_col, end_line, end_col)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		w.projectID, fileID, sym.QName, sym.Name, sym.Kind, sym.Signature, sym.Doc,
		sym.StartLine, sym.StartCol, sym.EndLine, sym.EndCol)
	if err != nil {
		return err
	}
	w.symIDs[sym.QName] = id
	_, err = w.tx.Exec(
		`INSERT INTO fts_symbol(name, qname, kind, symbol_id, project) VALUES(?,?,?,?,?)`,
		sym.Name, sym.QName, sym.Kind, id, w.project)
	return err
}

// popExisting returns and removes a surviving symbol id for a qname (greedy
// match, so duplicate qnames within one reprocessed file are handled one-by-one).
func popExisting(m map[string]int64, qname string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	id, ok := m[qname]
	if ok {
		delete(m, qname)
	}
	return id, ok
}

// RemoveFiles deletes all rows for files no longer on disk. Symbols cascade to
// their edges and references (FK cascade is on via the DSN pragma); FTS rows are
// virtual and must be cleared by hand, mirroring DeleteProject.
func (w *Writer) RemoveFiles(paths []string) error {
	for _, path := range paths {
		var fileID int64
		err := w.tx.QueryRow(`SELECT id FROM file WHERE project_id=? AND path=?`, w.projectID, path).Scan(&fileID)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := w.tx.Exec(
			`DELETE FROM fts_symbol WHERE symbol_id IN (SELECT id FROM symbol WHERE file_id=?)`, fileID); err != nil {
			return err
		}
		if _, err := w.tx.Exec(`DELETE FROM fts_code WHERE path=? AND project=?`, path, w.project); err != nil {
			return err
		}
		if _, err := w.tx.Exec(`DELETE FROM route WHERE file_id=?`, fileID); err != nil {
			return err
		}
		if _, err := w.tx.Exec(`DELETE FROM reference WHERE file_id=?`, fileID); err != nil {
			return err
		}
		if _, err := w.tx.Exec(`DELETE FROM symbol WHERE file_id=?`, fileID); err != nil {
			return err
		}
		if _, err := w.tx.Exec(`DELETE FROM file WHERE id=?`, fileID); err != nil {
			return err
		}
	}
	return nil
}

// deleteOrphans removes symbols that were present in a reprocessed file but no
// longer emitted (definitions removed from the file); their edges and references
// cascade away. Leftover entries in w.existing are exactly those not re-emitted.
func (w *Writer) deleteOrphans() error {
	for path := range w.touched {
		for _, id := range w.existing[path] {
			if _, err := w.tx.Exec(`DELETE FROM fts_symbol WHERE symbol_id=?`, id); err != nil {
				return err
			}
			if _, err := w.tx.Exec(`DELETE FROM symbol WHERE id=?`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// DefinedQNames returns every symbol qname currently defined in this project,
// read through the writer's own transaction so it reflects rows this refresh has
// already removed. It seeds the Go backend's `defined` set during a reload so a
// reprocessed package's references/edges into unchanged packages still resolve.
// It implements the DefinedQNames half of index.PriorIndex.
func (w *Writer) DefinedQNames() (map[string]bool, error) {
	rows, err := w.tx.Query(`SELECT qname FROM symbol WHERE project_id=?`, w.projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var qn string
		if err := rows.Scan(&qn); err != nil {
			return nil, err
		}
		out[qn] = true
	}
	return out, rows.Err()
}

// HeuristicNames returns bare-name -> defining qnames for this project's
// heuristic (tree-sitter) symbols, skipping files in exclude (those being
// reprocessed, whose old defs must not linger in the map). It rebuilds
// resolve.go's project-wide name map without re-parsing unchanged files, read
// through the writer's transaction. It implements the HeuristicNames half of
// index.PriorIndex.
func (w *Writer) HeuristicNames(exclude []string) (map[string][]string, error) {
	skip := make(map[string]bool, len(exclude))
	for _, p := range exclude {
		skip[p] = true
	}
	rows, err := w.tx.Query(`
		SELECT s.name, s.qname, f.path
		FROM symbol s JOIN file f ON f.id=s.file_id
		WHERE s.project_id=? AND f.fidelity=?`, w.projectID, heuristicFidelity)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var bare, qn, path string
		if err := rows.Scan(&bare, &qn, &path); err != nil {
			return nil, err
		}
		if skip[path] {
			continue
		}
		out[bare] = append(out[bare], qn)
	}
	return out, rows.Err()
}

// resolveSymID finds a qname's symbol id: this run's inserts first, then (in an
// incremental refresh only) any existing symbol in this project, so a reprocessed
// file's references/edges into unchanged files still resolve.
func (w *Writer) resolveSymID(qname string) (int64, bool, error) {
	if id, ok := w.symIDs[qname]; ok {
		return id, true, nil
	}
	if !w.incremental {
		return 0, false, nil
	}
	var id int64
	err := w.tx.QueryRow(`SELECT id FROM symbol WHERE qname=? AND project_id=? LIMIT 1`, qname, w.projectID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// Reference buffers a use-site for commit-time resolution.
func (w *Writer) Reference(r index.Reference) error { w.refs = append(w.refs, r); return nil }

// Edge buffers a relationship for commit-time resolution.
func (w *Writer) Edge(e index.Edge) error { w.edges = append(w.edges, e); return nil }

// Route buffers a route for commit-time resolution.
func (w *Writer) Route(r index.Route) error { w.routes = append(w.routes, r); return nil }

// Commit resolves buffered references/edges/routes against the symbol table and
// finalizes the transaction. In an incremental refresh it first drops symbols
// that a reprocessed file no longer defines.
func (w *Writer) Commit(vcsRev string) error {
	if w.incremental {
		if err := w.deleteOrphans(); err != nil {
			return w.fail(err)
		}
	}
	if err := w.commitRefs(); err != nil {
		return w.fail(err)
	}
	if err := w.commitEdges(); err != nil {
		return w.fail(err)
	}
	if err := w.commitRoutes(); err != nil {
		return w.fail(err)
	}
	if _, err := w.tx.Exec(`UPDATE project SET indexed_at=?, vcs_rev=? WHERE id=?`,
		time.Now().Unix(), vcsRev, w.projectID); err != nil {
		return w.fail(err)
	}
	return w.tx.Commit()
}

// commitRefs resolves buffered references to symbol ids (where known) and
// persists them.
func (w *Writer) commitRefs() error {
	for _, r := range w.refs {
		fileID, err := w.ensureFile(r.File)
		if err != nil {
			return err
		}
		var symID any
		if id, ok, err := w.resolveSymID(r.ToQName); err != nil {
			return err
		} else if ok {
			symID = id
		}
		if _, err := w.tx.Exec(
			`INSERT INTO reference(project_id, symbol_id, qname, file_id, line, col, role)
			 VALUES(?,?,?,?,?,?,?)`,
			w.projectID, symID, r.ToQName, fileID, r.Line, r.Col, r.Role); err != nil {
			return err
		}
	}
	return nil
}

// commitEdges persists CALLS edges, resolving each target to an intra-repo
// symbol or, failing that, a symbol in another indexed project (CROSS_CALLS).
func (w *Writer) commitEdges() error {
	for _, e := range w.edges {
		from, ok, err := w.resolveSymID(e.FromQName)
		if err != nil {
			return err
		}
		if !ok {
			continue // caller not defined in this repo
		}
		// Intra-repo: both ends in this project.
		to, ok, err := w.resolveSymID(e.ToQName)
		if err != nil {
			return err
		}
		if ok {
			if _, err := w.tx.Exec(
				`INSERT OR IGNORE INTO edge(src_id, dst_id, kind, confidence) VALUES(?,?,?,?)`,
				from, to, e.Kind, e.Confidence); err != nil {
				return err
			}
			continue
		}
		// Cross-repo: resolve the target qname against another indexed project.
		var dst int64
		err = w.tx.QueryRow(
			`SELECT id FROM symbol WHERE qname=? AND project_id<>? LIMIT 1`,
			e.ToQName, w.projectID).Scan(&dst)
		if err == sql.ErrNoRows {
			continue // stdlib or unindexed dependency — drop
		}
		if err != nil {
			return err
		}
		if _, err := w.tx.Exec(
			`INSERT OR IGNORE INTO edge(src_id, dst_id, kind, confidence) VALUES(?,?,?,?)`,
			from, dst, "CROSS_CALLS", e.Confidence); err != nil {
			return err
		}
	}
	return nil
}

// commitRoutes persists detected web routes, linking to the handler symbol id
// when it was defined in this repo.
func (w *Writer) commitRoutes() error {
	for _, rt := range w.routes {
		var handler any
		if id, ok, err := w.resolveSymID(rt.HandlerQName); err != nil {
			return err
		} else if ok {
			handler = id
		}
		var fileID any
		if rt.File != "" {
			id, err := w.ensureFile(rt.File)
			if err != nil {
				return err
			}
			fileID = id
		}
		if _, err := w.tx.Exec(
			`INSERT INTO route(project_id, method, pattern, handler_id, framework, file_id) VALUES(?,?,?,?,?,?)`,
			w.projectID, rt.Method, rt.Pattern, handler, rt.Framework, fileID); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) fail(err error) error {
	_ = w.tx.Rollback()
	return err
}

// Rollback aborts the indexing transaction.
func (w *Writer) Rollback() error { return w.tx.Rollback() }
