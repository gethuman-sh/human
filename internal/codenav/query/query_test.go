package query

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// indexFixture writes a tiny Go module (A → B → C call chain), indexes it into a
// fresh SQLite store, and returns the live *sql.DB plus the project name. It
// drives the full pipeline (index → store) so the read queries have real data.
func indexFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n\ngo 1.21\n",
		"main.go": `package main

// A calls B.
func A() { B() }

// B calls C.
func B() { C() }

// C does nothing.
func C() {}

func main() { A() }
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dbPath := filepath.Join(t.TempDir(), "codenav.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	scan := index.RepoScan{Project: "fix", Root: root}
	backends := index.PickFor(scan)
	if len(backends) == 0 {
		t.Fatal("no indexer matched the fixture")
	}
	w, err := st.NewWriter(scan.Project, scan.Root)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, ix := range backends {
		if err := ix.Index(context.Background(), scan, w); err != nil {
			t.Fatalf("index with %s: %v", ix.Name(), err)
		}
	}
	if err := w.Commit(""); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return st.DB(), scan.Project
}

func TestQueries_definitionsAndReferences(t *testing.T) {
	db, repo := indexFixture(t)

	defs, err := Def(db, "A", true)
	if err != nil {
		t.Fatalf("Def: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("Def(A) returned no hits")
	}
	if defs[0].Kind != "func" {
		t.Errorf("Def(A) kind = %q, want func", defs[0].Kind)
	}

	if _, err := Def(db, "A", false); err != nil { // body-off path
		t.Fatalf("Def body-off: %v", err)
	}

	refs, err := Refs(db, "B")
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if len(refs) == 0 {
		t.Error("Refs(B) returned no references (A calls B)")
	}

	syms, err := ListSymbols(db, repo, "func", 100)
	if err != nil {
		t.Fatalf("ListSymbols: %v", err)
	}
	if len(syms) < 4 {
		t.Errorf("ListSymbols got %d funcs, want >=4", len(syms))
	}

	out, err := Outline(db, "main.go", repo)
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	if len(out) == 0 {
		t.Error("Outline(main.go) returned no symbols")
	}

	rng, err := SymbolsInRange(db, repo, "main.go", 1, 5)
	if err != nil {
		t.Fatalf("SymbolsInRange: %v", err)
	}
	if len(rng) == 0 {
		t.Error("SymbolsInRange returned nothing for lines 1-5")
	}
}

func TestQueries_docSurfaced(t *testing.T) {
	db, repo := indexFixture(t)

	defs, err := Def(db, "B", false) // compact path, no body
	if err != nil {
		t.Fatalf("Def: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("Def(B) returned no hits")
	}
	if !strings.Contains(defs[0].Doc, "B calls C") {
		t.Errorf("Def(B).Doc = %q, want it to contain %q", defs[0].Doc, "B calls C")
	}

	out, err := Outline(db, "main.go", repo)
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	var found bool
	for _, s := range out {
		if s.Name == "C" && strings.Contains(s.Doc, "C does nothing") {
			found = true
		}
	}
	if !found {
		t.Error("Outline(main.go) did not surface C's doc comment")
	}
}

func TestQueries_search(t *testing.T) {
	db, repo := indexFixture(t)

	bySym, err := SearchSymbols(db, "A", repo, 25)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(bySym) == 0 {
		t.Error("SearchSymbols(A) returned nothing")
	}

	// Code search should not error even when it returns no hits.
	if _, err := SearchCode(db, "calls", repo, 25); err != nil {
		t.Fatalf("SearchCode: %v", err)
	}

	ov, err := GetOverview(db, repo, 12)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if ov.Kinds["func"] == 0 {
		t.Error("overview reports no funcs")
	}

	if _, err := ListRoutes(db, repo); err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
}

func TestQueries_callGraph(t *testing.T) {
	db, _ := indexFixture(t)

	callers, err := Callers(db, "C", 5)
	if err != nil {
		t.Fatalf("Callers: %v", err)
	}
	if len(callers) == 0 {
		t.Error("Callers(C) returned nothing (B and A call into C)")
	}

	callees, err := Callees(db, "A", 5)
	if err != nil {
		t.Fatalf("Callees: %v", err)
	}
	if len(callees) == 0 {
		t.Error("Callees(A) returned nothing (A calls B → C)")
	}

	paths, err := CallPath(db, "A", "C", 12, 8)
	if err != nil {
		t.Fatalf("CallPath: %v", err)
	}
	if len(paths) == 0 {
		t.Error("CallPath(A → C) found no path")
	}

	impacted, err := Impact(db, []string{"example.com/fix.C"}, 8)
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if len(impacted) == 0 {
		t.Error("Impact(C) returned no transitive callers")
	}
}
