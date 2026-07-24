package index

import (
	"context"
	"path/filepath"
	"testing"
)

// fakePrior is a fixed PriorIndex for driving IndexIncremental without a store.
type fakePrior struct {
	defined map[string]bool
	names   map[string][]string
}

func (f fakePrior) DefinedQNames() (map[string]bool, error) { return f.defined, nil }
func (f fakePrior) HeuristicNames(exclude []string) (map[string][]string, error) {
	return f.names, nil
}

func TestGoNative_indexIncremental_scopes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/inc\n\ngo 1.21\n")
	// Package "sub" defines Helper; the root package calls it from main.go.
	writeFile(t, filepath.Join(dir, "sub", "sub.go"), "package sub\n\nfunc Helper() {}\n")
	writeFile(t, filepath.Join(dir, "main.go"),
		"package main\n\nimport \"example.com/inc/sub\"\n\nfunc use() { sub.Helper() }\n\nfunc main() { use() }\n")

	sink := newCollectSink()
	scan := RepoScan{Project: "inc", Root: dir}
	// Only the root dir changed; sub is unchanged. Seed `defined` with sub.Helper
	// (as the DB would) so the cross-package reference/edge still resolves.
	prior := fakePrior{defined: map[string]bool{"example.com/inc/sub.Helper": true}}
	delta := Delta{Modified: []string{"main.go"}}
	if err := (GoNative{}).IndexIncremental(context.Background(), scan, delta, prior, sink); err != nil {
		t.Fatal(err)
	}

	// Only the reprocessed package's symbols are emitted (not sub's).
	if _, ok := sink.symbols["example.com/inc.use"]; !ok {
		t.Errorf("expected root-package symbol use to be emitted")
	}
	if _, ok := sink.symbols["example.com/inc/sub.Helper"]; ok {
		t.Errorf("unchanged package sub must not be re-emitted")
	}

	// The reference/edge into the unchanged package still resolves because
	// `defined` was seeded from the prior index.
	if !sink.edges[[2]string{"example.com/inc.use", "example.com/inc/sub.Helper"}] {
		t.Errorf("cross-package CALLS edge into seeded qname missing (edges=%v)", sink.edges)
	}
	foundRef := false
	for _, r := range sink.refs {
		if r.ToQName == "example.com/inc/sub.Helper" {
			foundRef = true
		}
	}
	if !foundRef {
		t.Errorf("expected a reference to the seeded unchanged-package qname")
	}
}

func TestGoNative_indexIncremental_noGoChange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/inc\n\ngo 1.21\n")
	sink := newCollectSink()
	// A delta touching only a non-Go file yields no package dirs -> no-op.
	delta := Delta{Modified: []string{"app.py"}}
	if err := (GoNative{}).IndexIncremental(context.Background(), RepoScan{Project: "inc", Root: dir}, delta, fakePrior{}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.symbols) != 0 || len(sink.files) != 0 {
		t.Errorf("no-Go-change delta should emit nothing, got %d symbols", len(sink.symbols))
	}
}

func TestGoNative_routeFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/svc\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "net/http"

func getUser(w http.ResponseWriter, r *http.Request) {}

func main() {
	http.HandleFunc("/users", getUser)
}
`)
	sink := newCollectSink()
	if err := (GoNative{}).Index(context.Background(), RepoScan{Project: "svc", Root: dir}, sink); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range sink.routes {
		if r.Pattern == "/users" {
			found = true
			if r.File != "main.go" {
				t.Errorf("route File = %q, want main.go", r.File)
			}
		}
	}
	if !found {
		t.Fatalf("route for /users not emitted (routes=%+v)", sink.routes)
	}
}

func TestGoPackageDirs(t *testing.T) {
	got := goPackageDirs(Delta{
		Added:    []string{"a.go", "sub/b.go"},
		Modified: []string{"sub/c.go", "notgo.py"},
		Deleted:  []string{"sub/b.go"},
	})
	want := map[string]bool{"./": true, "./sub": true}
	if len(got) != len(want) {
		t.Fatalf("goPackageDirs = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected pattern %q", p)
		}
	}
	if goPackageDirs(Delta{Modified: []string{"x.py"}}) != nil {
		t.Error("a delta with no .go files must yield nil")
	}
}
