package index

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes body to path, creating parent dirs. Shared by index tests
// that need to place a source file at an arbitrary depth.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSources_stat(t *testing.T) {
	dir := writePolyglot(t)
	got := ScanSources(RepoScan{Root: dir})

	byRel := map[string]OnDiskFile{}
	for _, f := range got {
		byRel[f.Rel] = f
	}
	// The polyglot fixture's curated sources plus its Go file must all appear.
	for _, want := range []string{"py/app.py", "web/app.ts", "skip/big.go"} {
		f, ok := byRel[want]
		if !ok {
			t.Fatalf("ScanSources missing %q (got %v)", want, keysOf(byRel))
		}
		if f.Size <= 0 {
			t.Errorf("%q Size = %d, want > 0", want, f.Size)
		}
		if f.MTime <= 0 {
			t.Errorf("%q MTime = %d, want > 0", want, f.MTime)
		}
		if !filepath.IsAbs(f.Abs) {
			t.Errorf("%q Abs = %q, want absolute", want, f.Abs)
		}
	}
}

func TestScanSources_skipsSkipDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.py"), "def a():\n    pass\n")
	writeFile(t, filepath.Join(dir, "node_modules", "dep.py"), "def b():\n    pass\n")

	for _, f := range ScanSources(RepoScan{Root: dir}) {
		if f.Rel == "node_modules/dep.py" {
			t.Fatalf("ScanSources descended into a skipDir: %q", f.Rel)
		}
	}
}

func keysOf(m map[string]OnDiskFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
