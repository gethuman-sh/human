// Package query is the single source of truth for codenav's read operations:
// search, definition, references, callers, callees and call-path. The CLI
// (and, later, MCP/HTTP) are thin presenters over these functions.
package query

import (
	"bufio"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gethuman-sh/human/internal/codenav/graph"
)

// ftsMatch turns free-form user input into a safe FTS5 MATCH expression by
// quoting each whitespace-separated token as a literal (implicit AND). This
// avoids syntax errors from characters FTS5 treats specially (- " * : ( etc.).
func ftsMatch(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return "\"\""
	}
	for i, f := range fields {
		fields[i] = "\"" + strings.ReplaceAll(f, "\"", "\"\"") + "\""
	}
	return strings.Join(fields, " ")
}

// SymbolHit is a resolved definition, optionally with a source snippet.
// EndLine makes the hit a precise edit target: callers that want to change the
// symbol can address File:Line-EndLine with their own editor instead of first
// reading the whole file to locate it.
type SymbolHit struct {
	QName     string `json:"qname"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line,omitempty"`
	Fidelity  string `json:"fidelity,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

// SearchHit is a search result (symbol-name or code-body).
type SearchHit struct {
	QName   string `json:"qname,omitempty"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// RefHit is a use-site of a symbol. In names the symbol that encloses the
// reference and Text is the trimmed source line, so one refs call answers
// "who uses this, and how" without opening each file.
type RefHit struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Role string `json:"role,omitempty"`
	In   string `json:"in,omitempty"`
	Text string `json:"text,omitempty"`
}

type symRow struct {
	id                 int64
	qname, name, kind  string
	sig, file, root    string
	startLine, endLine int
	fidelity           string
}

func resolveSymbols(db *sql.DB, ident string) ([]symRow, error) {
	const base = `
		SELECT s.id, s.qname, s.name, s.kind, COALESCE(s.signature,''),
		       f.path, p.root_path, s.start_line, s.end_line, COALESCE(f.fidelity,'')
		FROM symbol s
		JOIN file f    ON f.id = s.file_id
		JOIN project p ON p.id = s.project_id
		WHERE `
	for _, where := range []string{"s.qname = ?", "s.name = ?"} {
		rows, err := db.Query(base+where+" ORDER BY s.qname", ident)
		if err != nil {
			return nil, err
		}
		got, err := scanSymRows(rows)
		if err != nil {
			return nil, err
		}
		if len(got) > 0 {
			return got, nil
		}
	}
	return nil, nil
}

func scanSymRows(rows *sql.Rows) ([]symRow, error) {
	defer func() { _ = rows.Close() }()
	var out []symRow
	for rows.Next() {
		var r symRow
		if err := rows.Scan(&r.id, &r.qname, &r.name, &r.kind, &r.sig,
			&r.file, &r.root, &r.startLine, &r.endLine, &r.fidelity); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSymbols lists definitions (an entry point for exploring a codebase cold),
// optionally filtered by repo and kind. limit <= 0 means no limit.
func ListSymbols(db *sql.DB, repo, kind string, limit int) ([]SymbolHit, error) {
	stmt := `
		SELECT s.qname, s.name, s.kind, COALESCE(s.signature,''), f.path, s.start_line, s.end_line, COALESCE(f.fidelity,'')
		FROM symbol s
		JOIN file f    ON f.id = s.file_id
		JOIN project p ON p.id = s.project_id
		WHERE 1=1`
	var args []any
	if repo != "" {
		stmt += " AND p.name = ?"
		args = append(args, repo)
	}
	if kind != "" {
		stmt += " AND s.kind = ?"
		args = append(args, kind)
	}
	stmt += " ORDER BY s.qname"
	if limit > 0 {
		stmt += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SymbolHit
	for rows.Next() {
		var h SymbolHit
		if err := rows.Scan(&h.QName, &h.Name, &h.Kind, &h.Signature, &h.File, &h.Line, &h.EndLine, &h.Fidelity); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Hub is a heavily-called symbol — a good place to start understanding a repo.
type Hub struct {
	QName   string `json:"qname"`
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Callers int    `json:"callers"`
}

// Overview is a cold-start architecture summary: counts by kind and the most
// heavily-called symbols (entry points / hubs).
type Overview struct {
	Kinds map[string]int `json:"kinds"`
	Hubs  []Hub          `json:"hubs"`
}

// GetOverview computes an Overview, optionally scoped to one repo.
func GetOverview(db *sql.DB, repo string, hubLimit int) (*Overview, error) {
	ov := &Overview{Kinds: map[string]int{}}

	kindsSQL := `SELECT s.kind, COUNT(*) FROM symbol s JOIN project p ON p.id = s.project_id`
	hubsSQL := `
		SELECT s.qname, s.kind, f.path, s.start_line, COUNT(*) AS c
		FROM edge e
		JOIN symbol s  ON s.id = e.dst_id
		JOIN file f    ON f.id = s.file_id
		JOIN project p ON p.id = s.project_id
		WHERE e.kind = 'CALLS'`
	var kArgs, hArgs []any
	if repo != "" {
		kindsSQL += " WHERE p.name = ?"
		kArgs = append(kArgs, repo)
		hubsSQL += " AND p.name = ?"
		hArgs = append(hArgs, repo)
	}
	kindsSQL += " GROUP BY s.kind ORDER BY s.kind"
	hubsSQL += " GROUP BY e.dst_id ORDER BY c DESC, s.qname LIMIT ?"
	hArgs = append(hArgs, hubLimit)

	// Kinds (fully consumed before the next query — the pool serializes).
	rows, err := db.Query(kindsSQL, kArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ov.Kinds[k] = n
	}
	_ = rows.Close()

	rows, err = db.Query(hubsSQL, hArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h Hub
		if err := rows.Scan(&h.QName, &h.Kind, &h.File, &h.Line, &h.Callers); err != nil {
			return nil, err
		}
		ov.Hubs = append(ov.Hubs, h)
	}
	return ov, rows.Err()
}

// RouteHit is a detected web route, optionally bound to a handler symbol.
type RouteHit struct {
	Method    string `json:"method"`
	Pattern   string `json:"pattern"`
	Handler   string `json:"handler,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Framework string `json:"framework,omitempty"`
}

// ListRoutes lists detected routes (with handler location), optionally per repo.
func ListRoutes(db *sql.DB, repo string) ([]RouteHit, error) {
	stmt := `
		SELECT r.method, r.pattern, COALESCE(s.qname,''), COALESCE(f.path,''),
		       COALESCE(s.start_line,0), COALESCE(r.framework,'')
		FROM route r
		JOIN project p     ON p.id = r.project_id
		LEFT JOIN symbol s ON s.id = r.handler_id
		LEFT JOIN file f   ON f.id = s.file_id`
	var args []any
	if repo != "" {
		stmt += " WHERE p.name = ?"
		args = append(args, repo)
	}
	stmt += " ORDER BY r.pattern, r.method"
	rows, err := db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RouteHit
	for rows.Next() {
		var h RouteHit
		if err := rows.Scan(&h.Method, &h.Pattern, &h.Handler, &h.File, &h.Line, &h.Framework); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SymbolsInRange returns symbols in a file whose span overlaps [lo,hi] — used
// to map git-diff hunks back to the symbols they touch.
func SymbolsInRange(db *sql.DB, repo, file string, lo, hi int) ([]SymbolHit, error) {
	rows, err := db.Query(`
		SELECT s.qname, s.name, s.kind, COALESCE(s.signature,''), f.path, s.start_line, s.end_line, COALESCE(f.fidelity,'')
		FROM symbol s
		JOIN file f    ON f.id = s.file_id
		JOIN project p ON p.id = s.project_id
		WHERE p.name = ? AND f.path = ? AND s.start_line <= ? AND s.end_line >= ?
		ORDER BY s.start_line`, repo, file, hi, lo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SymbolHit
	for rows.Next() {
		var h SymbolHit
		if err := rows.Scan(&h.QName, &h.Name, &h.Kind, &h.Signature, &h.File, &h.Line, &h.EndLine, &h.Fidelity); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Outline lists the symbols defined in one file (signatures, no bodies) ordered
// by position — a cheap structural read that replaces dumping the whole file.
// file may be a repo-relative path or a bare basename (matched as a suffix).
func Outline(db *sql.DB, file, repo string) ([]SymbolHit, error) {
	stmt := `
		SELECT s.qname, s.name, s.kind, COALESCE(s.signature,''), f.path, s.start_line, s.end_line, COALESCE(f.fidelity,'')
		FROM symbol s
		JOIN file f    ON f.id = s.file_id
		JOIN project p ON p.id = s.project_id
		WHERE (f.path = ? OR f.path LIKE ?)`
	args := []any{file, "%/" + file}
	if repo != "" {
		stmt += " AND p.name = ?"
		args = append(args, repo)
	}
	stmt += " ORDER BY f.path, s.start_line"
	rows, err := db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SymbolHit
	for rows.Next() {
		var h SymbolHit
		if err := rows.Scan(&h.QName, &h.Name, &h.Kind, &h.Signature, &h.File, &h.Line, &h.EndLine, &h.Fidelity); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Impact returns the transitive callers (blast radius) of a set of seed symbols,
// i.e. what could break if the seeds change. Seeds are excluded from the result.
func Impact(db *sql.DB, seeds []string, depth int) ([]graph.Node, error) {
	seedSet := map[string]bool{}
	for _, s := range seeds {
		seedSet[s] = true
	}
	merged := map[int64]graph.Node{}
	for _, s := range seeds {
		nodes, err := Callers(db, s, depth)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if seedSet[n.QName] {
				continue
			}
			if cur, ok := merged[n.ID]; !ok || n.Depth < cur.Depth {
				merged[n.ID] = n
			}
		}
	}
	out := make([]graph.Node, 0, len(merged))
	for _, n := range merged {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].QName < out[j].QName
	})
	return out, nil
}

// Def returns matching definitions, each as a precise edit target (File +
// Line-EndLine). With includeBody it also attaches the source snippet; without
// it returns signature + location only, the token-frugal default for agents
// that just need to locate the symbol before editing with their own tools.
func Def(db *sql.DB, ident string, includeBody bool) ([]SymbolHit, error) {
	rows, err := resolveSymbols(db, ident)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolHit, 0, len(rows))
	for _, r := range rows {
		h := SymbolHit{
			QName: r.qname, Name: r.name, Kind: r.kind, Signature: r.sig,
			File: r.file, Line: r.startLine, EndLine: r.endLine, Fidelity: r.fidelity,
		}
		if includeBody {
			h.Snippet = readSnippet(r.root, r.file, r.startLine, r.endLine)
		}
		out = append(out, h)
	}
	return out, nil
}

// Refs returns every use-site of the matched symbol(s), each annotated with its
// enclosing symbol (In) and the trimmed source line (Text).
func Refs(db *sql.DB, ident string) ([]RefHit, error) {
	syms, err := resolveSymbols(db, ident)
	if err != nil || len(syms) == 0 {
		return nil, err
	}
	var out []RefHit
	for _, s := range syms {
		rows, err := db.Query(`
			SELECT f.path, r.line, r.col, COALESCE(r.role,''),
			       COALESCE((SELECT s2.qname FROM symbol s2
			                 WHERE s2.file_id = r.file_id
			                   AND s2.start_line <= r.line AND s2.end_line >= r.line
			                 ORDER BY s2.start_line DESC LIMIT 1), '')
			FROM reference r JOIN file f ON f.id = r.file_id
			WHERE r.symbol_id = ? ORDER BY f.path, r.line`, s.id)
		if err != nil {
			return nil, err
		}
		var refs []RefHit
		byFile := map[string]map[int]bool{}
		for rows.Next() {
			var h RefHit
			if err := rows.Scan(&h.File, &h.Line, &h.Col, &h.Role, &h.In); err != nil {
				_ = rows.Close()
				return nil, err
			}
			refs = append(refs, h)
			if byFile[h.File] == nil {
				byFile[h.File] = map[int]bool{}
			}
			byFile[h.File][h.Line] = true
		}
		_ = rows.Close()
		// Read each touched file once to attach source lines.
		text := make(map[string]map[int]string, len(byFile))
		for file, want := range byFile {
			text[file] = readLines(s.root, file, want)
		}
		for i := range refs {
			refs[i].Text = text[refs[i].File][refs[i].Line]
		}
		out = append(out, refs...)
	}
	return out, nil
}

// Callers returns symbols that transitively call ident.
func Callers(db *sql.DB, ident string, depth int) ([]graph.Node, error) {
	return traverse(db, ident, depth, graph.Callers)
}

// Callees returns symbols transitively called by ident.
func Callees(db *sql.DB, ident string, depth int) ([]graph.Node, error) {
	return traverse(db, ident, depth, graph.Callees)
}

func traverse(db *sql.DB, ident string, depth int, fn func(*sql.DB, int64, int) ([]graph.Node, error)) ([]graph.Node, error) {
	syms, err := resolveSymbols(db, ident)
	if err != nil || len(syms) == 0 {
		return nil, err
	}
	merged := map[int64]graph.Node{}
	for _, s := range syms {
		nodes, err := fn(db, s.id, depth)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if cur, ok := merged[n.ID]; !ok || n.Depth < cur.Depth {
				merged[n.ID] = n
			}
		}
	}
	out := make([]graph.Node, 0, len(merged))
	for _, n := range merged {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].QName < out[j].QName
	})
	return out, nil
}

// CallPath returns call paths from one symbol to another.
func CallPath(db *sql.DB, fromIdent, toIdent string, maxDepth, limit int) ([][]graph.Node, error) {
	from, err := resolveSymbols(db, fromIdent)
	if err != nil || len(from) == 0 {
		return nil, err
	}
	to, err := resolveSymbols(db, toIdent)
	if err != nil || len(to) == 0 {
		return nil, err
	}
	return graph.CallPaths(db, from[0].id, to[0].id, maxDepth, limit)
}

// SearchSymbols runs a BM25 symbol-name search.
func SearchSymbols(db *sql.DB, q, repo string, limit int) ([]SearchHit, error) {
	stmt := `
		SELECT s.qname, s.name, s.kind, f.path, s.start_line
		FROM fts_symbol fs
		JOIN symbol s ON s.id = fs.symbol_id
		JOIN file f   ON f.id = s.file_id
		WHERE fts_symbol MATCH ?`
	args := []any{ftsMatch(q)}
	if repo != "" {
		stmt += " AND fs.project = ?"
		args = append(args, repo)
	}
	stmt += " ORDER BY rank LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.QName, &h.Name, &h.Kind, &h.File, &h.Line); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchCode runs a BM25 full-text search over file bodies.
func SearchCode(db *sql.DB, q, repo string, limit int) ([]SearchHit, error) {
	stmt := `
		SELECT path, snippet(fts_code, 0, '«', '»', ' … ', 12)
		FROM fts_code
		WHERE fts_code MATCH ?`
	args := []any{ftsMatch(q)}
	if repo != "" {
		stmt += " AND project = ?"
		args = append(args, repo)
	}
	stmt += " ORDER BY rank LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.File, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// readLines returns the trimmed text of the requested 1-based line numbers of a
// file, reading it in a single pass. Missing/unreadable lines are simply absent.
func readLines(root, rel string, want map[int]bool) map[int]string {
	out := make(map[int]string, len(want))
	if len(want) == 0 {
		return out
	}
	f, err := os.Open(filepath.Join(root, rel)) // #nosec G304 -- reads a source file from the indexed repo by recorded path
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if want[line] {
			out[line] = strings.TrimSpace(sc.Text())
			if len(out) == len(want) {
				break
			}
		}
	}
	return out
}

// readSnippet returns lines [start,end] of a file (capped), with line numbers.
func readSnippet(root, rel string, start, end int) string {
	if start <= 0 {
		return ""
	}
	if end < start {
		end = start
	}
	if end-start > 60 {
		end = start + 60 // cap runaway snippets
	}
	f, err := os.Open(filepath.Join(root, rel)) // #nosec G304 -- reads a source file from the indexed repo by recorded path
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var b []byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if line < start {
			continue
		}
		if line > end {
			break
		}
		b = append(b, sc.Bytes()...)
		b = append(b, '\n')
	}
	return string(b)
}
