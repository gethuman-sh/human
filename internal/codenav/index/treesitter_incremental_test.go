package index

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTreeSitter_indexIncremental(t *testing.T) {
	dir := t.TempDir()
	// caller.py calls shared(), defined in the unchanged helper.py. The DB-derived
	// name map (project-wide unique name) must let the cross-file call resolve.
	writeFile(t, filepath.Join(dir, "caller.py"), "def run():\n    shared()\n")
	writeFile(t, filepath.Join(dir, "helper.py"), "def shared():\n    return 1\n")

	sink := newCollectSink()
	scan := RepoScan{Project: "poly", Root: dir}
	prior := fakePrior{names: map[string][]string{"shared": {"helper.py:shared"}}}
	delta := Delta{Modified: []string{"caller.py"}}
	if err := (TreeSitter{}).IndexIncremental(context.Background(), scan, delta, prior, sink); err != nil {
		t.Fatal(err)
	}

	// Only the changed file is re-parsed/emitted.
	if _, ok := sink.symbols["caller.py:run"]; !ok {
		t.Errorf("expected caller.py:run to be emitted")
	}
	if _, ok := sink.symbols["helper.py:shared"]; ok {
		t.Errorf("unchanged helper.py must not be re-emitted")
	}

	// The cross-file call resolves via the DB-derived unique-name map.
	if !sink.edges[[2]string{"caller.py:run", "helper.py:shared"}] {
		t.Errorf("cross-file heuristic edge missing (edges=%v)", sink.edges)
	}
}

func TestTreeSitter_indexIncremental_noNonGoChange(t *testing.T) {
	dir := t.TempDir()
	sink := newCollectSink()
	delta := Delta{Modified: []string{"main.go"}} // Go is not TreeSitter's job
	if err := (TreeSitter{}).IndexIncremental(context.Background(), RepoScan{Project: "poly", Root: dir}, delta, fakePrior{}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.symbols) != 0 {
		t.Errorf("a Go-only delta should emit nothing from TreeSitter, got %d", len(sink.symbols))
	}
}

func TestCuratedNonGo(t *testing.T) {
	got := curatedNonGo("/root", []string{"a.py", "b.ts", "c.go", "d.txt"})
	if len(got) != 2 {
		t.Fatalf("curatedNonGo = %v, want 2 curated non-Go files", got)
	}
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Errorf("curatedNonGo returned non-absolute path %q", p)
		}
	}
}
