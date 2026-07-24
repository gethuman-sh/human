package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/internal/codenav/graph"
	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/query"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// hasQName reports whether any node carries the given qname.
func hasQName(nodes []graph.Node, qname string) bool {
	for _, n := range nodes {
		if n.QName == qname {
			return true
		}
	}
	return false
}

// seedHeuristic writes two heuristic (tree-sitter-style) files with real bytes
// on disk under root, so File records size/mtime and fts_code, and returns the
// open store plus its root.
func seedHeuristic(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def A():\n    B()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.py"), []byte("def B():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	w, err := st.NewWriter("p", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "a.py", Lang: "python", ContentHash: "ha", Fidelity: index.Heuristic}); err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "b.py", Lang: "python", ContentHash: "hb", Fidelity: index.Heuristic}); err != nil {
		t.Fatal(err)
	}
	if err := w.Symbol(index.Symbol{QName: "a.py:A", Name: "A", Kind: "func", File: "a.py", StartLine: 1, EndLine: 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Symbol(index.Symbol{QName: "b.py:B", Name: "B", Kind: "func", File: "b.py", StartLine: 1, EndLine: 2}); err != nil {
		t.Fatal(err)
	}
	// A route registered in a.py, and a cross-file edge A -> B.
	if err := w.Route(index.Route{Method: "GET", Pattern: "/x", HandlerQName: "a.py:A", Framework: "flask", File: "a.py"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Edge(index.Edge{FromQName: "a.py:A", ToQName: "b.py:B", Kind: "CALLS", Confidence: 0.6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("rev1"); err != nil {
		t.Fatal(err)
	}
	return st, root
}

func TestProjectExistsAndFiles(t *testing.T) {
	st, _ := seedHeuristic(t)

	if ok, err := st.ProjectExists("p"); err != nil || !ok {
		t.Fatalf("ProjectExists(p) = %v,%v; want true", ok, err)
	}
	if ok, err := st.ProjectExists("nope"); err != nil || ok {
		t.Fatalf("ProjectExists(nope) = %v,%v; want false", ok, err)
	}

	files, err := st.ProjectFiles("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("ProjectFiles = %v, want 2 entries", files)
	}
	a, ok := files["a.py"]
	if !ok {
		t.Fatal("a.py missing from ProjectFiles")
	}
	if a.Hash != "ha" {
		t.Errorf("a.py hash = %q, want ha", a.Hash)
	}
	if a.Size <= 0 || a.MTime <= 0 {
		t.Errorf("a.py size/mtime = %d/%d, want both > 0 (recorded from disk)", a.Size, a.MTime)
	}
}

func TestWriterPriorReads(t *testing.T) {
	st, root := seedHeuristic(t)

	w, err := st.NewIncrementalWriter("p", root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Rollback() }()

	defined, err := w.DefinedQNames()
	if err != nil {
		t.Fatal(err)
	}
	if !defined["a.py:A"] || !defined["b.py:B"] {
		t.Errorf("DefinedQNames = %v, want both A and B", defined)
	}

	// Excluding a.py drops its defs from the heuristic name map.
	names, err := w.HeuristicNames([]string{"a.py"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names["A"]; ok {
		t.Errorf("HeuristicNames should have excluded a.py's A, got %v", names)
	}
	if got := names["B"]; len(got) != 1 || got[0] != "b.py:B" {
		t.Errorf("HeuristicNames[B] = %v, want [b.py:B]", got)
	}
}

// must fails the test on a non-nil error, keeping the callers' branching (and
// thus cyclomatic complexity) low.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func symIDOf(t *testing.T, st *store.Store, qname string) int64 {
	t.Helper()
	var id int64
	must(t, st.DB().QueryRow(`SELECT id FROM symbol WHERE qname=?`, qname).Scan(&id))
	return id
}

// reprocessAPy reloads a.py re-emitting A, its route, and the cross-file edge to
// the unchanged b.py (which must resolve via the DB fallback).
func reprocessAPy(t *testing.T, st *store.Store, root string) {
	t.Helper()
	w, err := st.NewIncrementalWriter("p", root)
	must(t, err)
	must(t, w.File(index.FileRec{Path: "a.py", Lang: "python", ContentHash: "ha2", Fidelity: index.Heuristic}))
	must(t, w.Symbol(index.Symbol{QName: "a.py:A", Name: "A", Kind: "func", File: "a.py", StartLine: 1, EndLine: 3}))
	must(t, w.Route(index.Route{Method: "GET", Pattern: "/x", HandlerQName: "a.py:A", Framework: "flask", File: "a.py"}))
	must(t, w.Edge(index.Edge{FromQName: "a.py:A", ToQName: "b.py:B", Kind: "CALLS", Confidence: 0.6}))
	must(t, w.Commit("rev2"))
}

// TestIncrementalWriter_routeAndCrossFileEdge reprocesses a.py: its route is
// recomputed with a file_id, and its cross-file edge into the unchanged b.py
// resolves via the DB (resolveSymID fallback), keeping B's id stable.
func TestIncrementalWriter_routeAndCrossFileEdge(t *testing.T) {
	st, root := seedHeuristic(t)
	idB := symIDOf(t, st, "b.py:B")

	reprocessAPy(t, st, root)

	if got := symIDOf(t, st, "b.py:B"); got != idB {
		t.Errorf("B id changed %d -> %d across reprocessing", idB, got)
	}
	callees, err := query.Callees(st.DB(), "a.py:A", 5)
	must(t, err)
	if !hasQName(callees, "b.py:B") {
		t.Errorf("cross-file edge A->B lost after reprocessing a.py (callees=%v)", callees)
	}

	routes, err := query.ListRoutes(st.DB(), "p")
	must(t, err)
	if len(routes) != 1 || routes[0].Pattern != "/x" || routes[0].Handler != "a.py:A" {
		t.Fatalf("routes = %+v, want one /x -> a.py:A", routes)
	}
}

// TestRemoveFilesUnknownPath is a no-op when the path was never indexed.
func TestRemoveFilesUnknownPath(t *testing.T) {
	st, root := seedHeuristic(t)
	w, err := st.NewIncrementalWriter("p", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RemoveFiles([]string{"ghost.py"}); err != nil {
		t.Fatalf("RemoveFiles on unknown path should be a no-op, got %v", err)
	}
	if err := w.Commit("rev2"); err != nil {
		t.Fatal(err)
	}
	if d, _ := query.Def(st.DB(), "a.py:A", false); len(d) != 1 {
		t.Errorf("existing symbol should be untouched, got %v", d)
	}
}

// TestNewIncrementalWriter_missingProject errors so the caller can fall back to
// a full rebuild.
func TestNewIncrementalWriter_missingProject(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.NewIncrementalWriter("absent", t.TempDir()); err == nil {
		t.Fatal("NewIncrementalWriter on a non-existent project should error")
	}
}
