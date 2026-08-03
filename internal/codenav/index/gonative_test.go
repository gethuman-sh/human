package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// collectSink records everything a backend emits, for assertions.
type collectSink struct {
	symbols map[string]Symbol
	edges   map[[2]string]bool
	refs    []Reference
	files   []FileRec
	routes  []Route
}

func newCollectSink() *collectSink {
	return &collectSink{symbols: map[string]Symbol{}, edges: map[[2]string]bool{}}
}

func (c *collectSink) File(f FileRec) error        { c.files = append(c.files, f); return nil }
func (c *collectSink) Symbol(s Symbol) error       { c.symbols[s.QName] = s; return nil }
func (c *collectSink) Reference(r Reference) error { c.refs = append(c.refs, r); return nil }
func (c *collectSink) Edge(e Edge) error {
	c.edges[[2]string{e.FromQName, e.ToQName}] = true
	return nil
}
func (c *collectSink) Route(r Route) error { c.routes = append(c.routes, r); return nil }

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n\ngo 1.21\n",
		"main.go": `package main

// A calls B.
func A() { B() }

// B calls C.
func B() { C() }

func C() {}

func main() { A() }
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGoNativeIndex(t *testing.T) {
	dir := writeFixture(t)
	sink := newCollectSink()
	scan := RepoScan{Project: "fix", Root: dir}

	if !(GoNative{}).CanHandle(scan) {
		t.Fatal("GoNative should handle a dir with go.mod")
	}
	if err := (GoNative{}).Index(context.Background(), scan, sink); err != nil {
		t.Fatal(err)
	}

	for _, qn := range []string{
		"example.com/fix.A", "example.com/fix.B",
		"example.com/fix.C", "example.com/fix.main",
	} {
		if _, ok := sink.symbols[qn]; !ok {
			t.Errorf("missing symbol %s (got %d symbols)", qn, len(sink.symbols))
		}
	}

	wantEdges := [][2]string{
		{"example.com/fix.main", "example.com/fix.A"},
		{"example.com/fix.A", "example.com/fix.B"},
		{"example.com/fix.B", "example.com/fix.C"},
	}
	for _, e := range wantEdges {
		if !sink.edges[e] {
			t.Errorf("missing CALLS edge %s -> %s", e[0], e[1])
		}
	}

	// Symbols carry precise signatures and positions.
	if a := sink.symbols["example.com/fix.A"]; a.Kind != "func" || a.StartLine == 0 {
		t.Errorf("symbol A = %+v, want kind=func with a start line", a)
	}
	if len(sink.files) == 0 {
		t.Error("expected at least one FileRec")
	}
}

func TestGoNativeIndex_capturesDoc(t *testing.T) {
	dir := writeFixture(t)
	sink := newCollectSink()
	if err := (GoNative{}).Index(context.Background(), RepoScan{Project: "fix", Root: dir}, sink); err != nil {
		t.Fatal(err)
	}
	sym, ok := sink.symbols["example.com/fix.B"]
	if !ok {
		t.Fatal("missing symbol example.com/fix.B")
	}
	if !strings.Contains(sym.Doc, "B calls C") {
		t.Errorf("Symbol.Doc = %q, want it to contain the intent comment %q", sym.Doc, "B calls C")
	}
}
