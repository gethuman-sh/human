package codenav_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/codenav/query"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

func mustIndex(t *testing.T, st *store.Store, root string, full bool) codenav.IndexResult {
	t.Helper()
	res, err := codenav.IndexProject(context.Background(), st, "fix", root, full)
	if err != nil {
		t.Fatalf("IndexProject(full=%v): %v", full, err)
	}
	return res
}

func writeSrc(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symIDByQName(t *testing.T, st *store.Store, qname string) int64 {
	t.Helper()
	var id int64
	if err := st.DB().QueryRow(`SELECT id FROM symbol WHERE qname=?`, qname).Scan(&id); err != nil {
		t.Fatalf("symbol %q not found: %v", qname, err)
	}
	return id
}

// TestIndexProject_incrementalEditedFile is the AC-2 byte-for-byte guard for
// definitions/references/search: after editing one file, an incremental refresh
// must produce query output identical to a from-scratch --full rebuild of the
// same end state.
func TestIndexProject_incrementalEditedFile(t *testing.T) {
	root := writeGoRepo(t)
	incSt := openStore(t)
	mustIndex(t, incSt, root, false) // first index is a full rebuild internally

	// Edit main.go: B now calls a new C, adding a symbol and a CALLS edge.
	writeSrc(t, root, "main.go", `package main

func A() { B() }

func B() { C() }

func C() {}

func main() { A() }
`)
	if res := mustIndex(t, incSt, root, false); res.Skipped {
		t.Fatal("editing a file must not be skipped")
	}

	// Full rebuild of the same end state in a separate store.
	fullSt := openStore(t)
	mustIndex(t, fullSt, root, true)

	assertQueriesEqual(t, incSt, fullSt)
	// The new symbol and edge are present in the incremental store.
	if d, _ := query.Def(incSt.DB(), "example.com/fix.C", false); len(d) != 1 {
		t.Errorf("new symbol C should be queryable after incremental edit, got %v", d)
	}
}

func TestIndexProject_touchNoContentChange(t *testing.T) {
	root := writeGoRepo(t)
	st := openStore(t)
	mustIndex(t, st, root, false)

	// Rewrite main.go with identical bytes and bump its mtime: the cheap stat
	// differs, forcing a hash comparison that finds no change -> skipped.
	main := filepath.Join(root, "main.go")
	body, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, body, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(main, future, future); err != nil {
		t.Fatal(err)
	}
	if res := mustIndex(t, st, root, false); !res.Skipped {
		t.Error("touch with identical content should be skipped")
	}
}

func TestIndexProject_addFile(t *testing.T) {
	root := writeGoRepo(t)
	st := openStore(t)
	mustIndex(t, st, root, false)
	idA := symIDByQName(t, st, "example.com/fix.A")

	writeSrc(t, root, "helper.go", "package main\n\nfunc Helper() {}\n")
	if res := mustIndex(t, st, root, false); res.Skipped {
		t.Fatal("adding a file must not be skipped")
	}

	if d, _ := query.Def(st.DB(), "example.com/fix.Helper", false); len(d) != 1 {
		t.Errorf("added symbol Helper should be queryable, got %v", d)
	}
	// Pre-existing symbols keep their identity (stable ids, AD-2).
	if got := symIDByQName(t, st, "example.com/fix.A"); got != idA {
		t.Errorf("symbol A's id changed on add (%d -> %d); identity must be stable", idA, got)
	}

	// Byte-for-byte against a full rebuild of the end state.
	fullSt := openStore(t)
	mustIndex(t, fullSt, root, true)
	assertQueriesEqual(t, st, fullSt)
}

func TestIndexProject_deleteFile(t *testing.T) {
	root := writeGoRepo(t)
	// helper.go is an independent file (not referenced by main.go) so deleting it
	// leaves the package compiling.
	writeSrc(t, root, "helper.go", "package main\n\nfunc Helper() {}\n")
	st := openStore(t)
	mustIndex(t, st, root, false)

	if err := os.Remove(filepath.Join(root, "helper.go")); err != nil {
		t.Fatal(err)
	}
	if res := mustIndex(t, st, root, false); res.Skipped {
		t.Fatal("deleting a file must not be skipped")
	}

	if d, _ := query.Def(st.DB(), "example.com/fix.Helper", false); len(d) != 0 {
		t.Errorf("deleted symbol Helper must be gone, got %v", d)
	}
	if d, _ := query.Def(st.DB(), "example.com/fix.A", false); len(d) != 1 {
		t.Errorf("surviving symbol A should remain, got %v", d)
	}

	fullSt := openStore(t)
	mustIndex(t, fullSt, root, true)
	assertQueriesEqual(t, st, fullSt)
}

func TestIndexProject_deletePackageDir(t *testing.T) {
	root := writeGoRepo(t)
	// A standalone sub-package, not imported by main, so the dir can be removed
	// wholesale without breaking the build.
	writeSrc(t, root, "sub/sub.go", "package sub\n\nfunc Sub() {}\n")
	st := openStore(t)
	mustIndex(t, st, root, false)
	if d, _ := query.Def(st.DB(), "example.com/fix/sub.Sub", false); len(d) != 1 {
		t.Fatalf("precondition: sub.Sub should be indexed, got %v", d)
	}

	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	// Must not fatally error on the emptied/absent package dir.
	if res := mustIndex(t, st, root, false); res.Skipped {
		t.Fatal("removing a package dir must not be skipped")
	}
	if d, _ := query.Def(st.DB(), "example.com/fix/sub.Sub", false); len(d) != 0 {
		t.Errorf("rows for the removed package dir must be gone, got %v", d)
	}
}

// TestIndexProject_backendErrorRollsBack: a Go module with no .go files makes
// the Go backend fail the full-rebuild path, which must roll back and surface
// the error rather than leave a half-written project.
func TestIndexProject_backendErrorRollsBack(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, root, "go.mod", "module example.com/empty\n\ngo 1.21\n")
	st := openStore(t)
	if _, err := codenav.IndexProject(context.Background(), st, "fix", root, false); err == nil {
		t.Fatal("indexing a Go module with no Go files should error")
	}
	if ok, _ := st.ProjectExists("fix"); ok {
		t.Error("failed index must not leave a project row behind")
	}
}

// TestIndexProject_incrementalTreeSitter drives the incremental orchestration
// through the tree-sitter backend only (no go.mod), asserting byte-for-byte
// parity with a full rebuild after editing one non-Go file.
func TestIndexProject_incrementalTreeSitter(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, root, "a.py", "def A():\n    B()\n")
	writeSrc(t, root, "b.py", "def B():\n    pass\n")
	incSt := openStore(t)
	mustIndex(t, incSt, root, false)

	// Edit a.py; b.py is untouched.
	writeSrc(t, root, "a.py", "def A():\n    B()\n\ndef A2():\n    pass\n")
	if res := mustIndex(t, incSt, root, false); res.Skipped {
		t.Fatal("editing a.py must not be skipped")
	}

	fullSt := openStore(t)
	mustIndex(t, fullSt, root, true)

	la, _ := query.ListSymbols(incSt.DB(), "", "", 0)
	lb, _ := query.ListSymbols(fullSt.DB(), "", "", 0)
	if !reflect.DeepEqual(la, lb) {
		t.Fatalf("tree-sitter incremental ListSymbols differ:\n inc=%+v\n full=%+v", la, lb)
	}
}

// assertQueriesEqual asserts the AC-2 read paths match between two stores.
// Definitions, references, and full-text search are compared byte-for-byte; the
// call graph is compared by normalized nodes (row ids differ between stores).
func assertQueriesEqual(t *testing.T, a, b *store.Store) {
	t.Helper()

	la, _ := query.ListSymbols(a.DB(), "", "", 0)
	lb, _ := query.ListSymbols(b.DB(), "", "", 0)
	if !reflect.DeepEqual(la, lb) {
		t.Fatalf("ListSymbols differ:\n incremental=%+v\n full=%+v", la, lb)
	}

	for _, s := range la {
		ra, _ := query.Refs(a.DB(), s.QName)
		rb, _ := query.Refs(b.DB(), s.QName)
		if !reflect.DeepEqual(ra, rb) {
			t.Errorf("Refs(%s) differ:\n incremental=%+v\n full=%+v", s.QName, ra, rb)
		}
		da, _ := query.Def(a.DB(), s.QName, true)
		db, _ := query.Def(b.DB(), s.QName, true)
		if !reflect.DeepEqual(da, db) {
			t.Errorf("Def(%s) differ:\n incremental=%+v\n full=%+v", s.QName, da, db)
		}
		// No interface dispatch in these fixtures, so the call graph is exact too.
		if ca, cb := calleeQNames(t, a, s.QName), calleeQNames(t, b, s.QName); !reflect.DeepEqual(ca, cb) {
			t.Errorf("Callees(%s) differ:\n incremental=%v\n full=%v", s.QName, ca, cb)
		}
	}

	for _, term := range []string{"func", "A", "main"} {
		sca, _ := query.SearchCode(a.DB(), term, "", 20)
		scb, _ := query.SearchCode(b.DB(), term, "", 20)
		if !reflect.DeepEqual(sca, scb) {
			t.Errorf("SearchCode(%q) differ:\n incremental=%+v\n full=%+v", term, sca, scb)
		}
		ssa, _ := query.SearchSymbols(a.DB(), term, "", 20)
		ssb, _ := query.SearchSymbols(b.DB(), term, "", 20)
		if !reflect.DeepEqual(ssa, ssb) {
			t.Errorf("SearchSymbols(%q) differ:\n incremental=%+v\n full=%+v", term, ssa, ssb)
		}
	}
}

// calleeQNames returns the sorted "qname@depth" projection of Callees, dropping
// the row id (which legitimately differs between two separately-built stores).
func calleeQNames(t *testing.T, st *store.Store, qname string) []string {
	t.Helper()
	nodes, err := query.Callees(st.DB(), qname, 10)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.QName)
	}
	return out
}
