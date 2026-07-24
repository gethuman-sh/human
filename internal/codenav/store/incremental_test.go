package store_test

import (
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/query"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// writeABC seeds one file a.go with A -> B -> C and a reference to B, using the
// synthetic-symbol style (no Go toolchain). Returns the open store.
func writeABC(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	w, err := st.NewWriter("p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "a.go", Lang: "go", ContentHash: "h1", Fidelity: index.Precise}); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]string{{"pkg.A", "A"}, {"pkg.B", "B"}, {"pkg.C", "C"}} {
		if err := w.Symbol(index.Symbol{QName: s[0], Name: s[1], Kind: "func", File: "a.go", StartLine: 1, EndLine: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range [][2]string{{"pkg.A", "pkg.B"}, {"pkg.B", "pkg.C"}} {
		if err := w.Edge(index.Edge{FromQName: e[0], ToQName: e[1], Kind: "CALLS", Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Reference(index.Reference{ToQName: "pkg.B", File: "a.go", Line: 3, Role: "ref"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("rev1"); err != nil {
		t.Fatal(err)
	}
	return st
}

func symID(t *testing.T, st *store.Store, qname string) int64 {
	t.Helper()
	var id int64
	if err := st.DB().QueryRow(`SELECT id FROM symbol WHERE qname=?`, qname).Scan(&id); err != nil {
		t.Fatalf("symbol %q not found: %v", qname, err)
	}
	return id
}

func TestIncrementalWriter_reconcileStableID(t *testing.T) {
	st := writeABC(t)
	idB := symID(t, st, "pkg.B")

	// Reprocess a.go: A's signature changes but B and C survive. B must keep its
	// id so the reference from the unchanged part of the file still resolves.
	w, err := st.NewIncrementalWriter("p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "a.go", Lang: "go", ContentHash: "h2", Fidelity: index.Precise}); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][3]string{{"pkg.A", "A", "func A(x int)"}, {"pkg.B", "B", "func B()"}, {"pkg.C", "C", "func C()"}} {
		if err := w.Symbol(index.Symbol{QName: s[0], Name: s[1], Kind: "func", Signature: s[2], File: "a.go", StartLine: 1, EndLine: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Re-emit the same reference to B (as a reload would).
	if err := w.Reference(index.Reference{ToQName: "pkg.B", File: "a.go", Line: 3, Role: "ref"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("rev2"); err != nil {
		t.Fatal(err)
	}

	if got := symID(t, st, "pkg.B"); got != idB {
		t.Errorf("B's symbol id changed across reprocessing: was %d, now %d", idB, got)
	}
	refs, err := query.Refs(st.DB(), "pkg.B")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Line != 3 {
		t.Fatalf("refs(B) = %v, want one at line 3", refs)
	}
}

func TestIncrementalWriter_orphanRemoved(t *testing.T) {
	st := writeABC(t)

	// Reprocess a.go dropping the definition of C entirely.
	w, err := st.NewIncrementalWriter("p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "a.go", Lang: "go", ContentHash: "h2", Fidelity: index.Precise}); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]string{{"pkg.A", "A"}, {"pkg.B", "B"}} {
		if err := w.Symbol(index.Symbol{QName: s[0], Name: s[1], Kind: "func", File: "a.go", StartLine: 1, EndLine: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Edge(index.Edge{FromQName: "pkg.A", ToQName: "pkg.B", Kind: "CALLS", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("rev2"); err != nil {
		t.Fatal(err)
	}

	// C's symbol is gone; siblings retained.
	defs, err := query.Def(st.DB(), "pkg.C", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Errorf("orphan pkg.C should be gone, got %v", defs)
	}
	if d, _ := query.Def(st.DB(), "pkg.A", false); len(d) != 1 {
		t.Errorf("sibling pkg.A should survive, got %v", d)
	}
	// The B->C edge cascaded away with C.
	callees, err := query.Callees(st.DB(), "pkg.B", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range callees {
		if n.QName == "pkg.C" {
			t.Errorf("edge B->C should be gone after C removed")
		}
	}
}

func TestRemoveFiles(t *testing.T) {
	st := openStoreTwoFiles(t)

	w, err := st.NewIncrementalWriter("p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RemoveFiles([]string{"b.go"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("rev2"); err != nil {
		t.Fatal(err)
	}

	// b.go's symbol is gone, along with the reference into it and its file row.
	if defs, _ := query.Def(st.DB(), "pkg.B", false); len(defs) != 0 {
		t.Errorf("removed file's symbol pkg.B should be gone, got %v", defs)
	}
	var fileRows int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM file WHERE path='b.go'`).Scan(&fileRows); err != nil {
		t.Fatal(err)
	}
	if fileRows != 0 {
		t.Errorf("file row for b.go should be gone, got %d", fileRows)
	}
	// a.go's symbol survives.
	if defs, _ := query.Def(st.DB(), "pkg.A", false); len(defs) != 1 {
		t.Errorf("pkg.A in the untouched file should survive, got %v", defs)
	}
}

// openStoreTwoFiles seeds a.go (defines A, which references B) and b.go
// (defines B), so removing b.go must clear B and the reference to it.
func openStoreTwoFiles(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w, err := st.NewWriter("p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "a.go", Lang: "go", ContentHash: "h", Fidelity: index.Precise}); err != nil {
		t.Fatal(err)
	}
	if err := w.File(index.FileRec{Path: "b.go", Lang: "go", ContentHash: "h", Fidelity: index.Precise}); err != nil {
		t.Fatal(err)
	}
	if err := w.Symbol(index.Symbol{QName: "pkg.A", Name: "A", Kind: "func", File: "a.go", StartLine: 1, EndLine: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Symbol(index.Symbol{QName: "pkg.B", Name: "B", Kind: "func", File: "b.go", StartLine: 1, EndLine: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Reference(index.Reference{ToQName: "pkg.B", File: "a.go", Line: 2, Role: "ref"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("rev1"); err != nil {
		t.Fatal(err)
	}
	return st
}
