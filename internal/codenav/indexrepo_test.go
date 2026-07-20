package codenav_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// writeGoRepo lays down a minimal Go module so a real indexer backend matches
// and produces symbols, mirroring the fixture style in internal/codenav/index.
func writeGoRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n\ngo 1.21\n",
		"main.go": `package main

func A() { B() }

func B() {}

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

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "codenav.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestIndexProject_success(t *testing.T) {
	st := openStore(t)
	res, err := codenav.IndexProject(context.Background(), st, "fix", writeGoRepo(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped {
		t.Fatal("first index should not be skipped")
	}
	if res.Info.Symbols == 0 {
		t.Errorf("expected symbols indexed, got %d", res.Info.Symbols)
	}
}

// Indexing a committed git checkout records the HEAD sha, exercising gitRev's
// success path (a non-git dir yields "" instead).
func TestIndexProject_gitCheckout(t *testing.T) {
	root := writeGoRepo(t)
	for _, argv := range [][]string{
		{"init"}, {"config", "user.email", "t@t.t"}, {"config", "user.name", "t"},
		{"add", "."}, {"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, argv...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}
	res, err := codenav.IndexProject(context.Background(), openStore(t), "fix", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Info.VcsRev == "" {
		t.Error("expected a recorded HEAD sha for a committed checkout")
	}
}

func TestIndexProject_skipUnchanged(t *testing.T) {
	st := openStore(t)
	root := writeGoRepo(t)
	if _, err := codenav.IndexProject(context.Background(), st, "fix", root, false); err != nil {
		t.Fatal(err)
	}
	res, err := codenav.IndexProject(context.Background(), st, "fix", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Error("second index of unchanged source should be skipped")
	}
}

func TestIndexProject_fullForces(t *testing.T) {
	st := openStore(t)
	root := writeGoRepo(t)
	if _, err := codenav.IndexProject(context.Background(), st, "fix", root, false); err != nil {
		t.Fatal(err)
	}
	res, err := codenav.IndexProject(context.Background(), st, "fix", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped {
		t.Error("full=true must re-index even when the source is unchanged")
	}
}

// A directory with no recognized sources still matches the TreeSitter
// catch-all backend, so it indexes cleanly to zero symbols rather than
// erroring — the graceful path callers rely on for empty checkouts.
func TestIndexProject_emptyDir(t *testing.T) {
	st := openStore(t)
	res, err := codenav.IndexProject(context.Background(), st, "empty", t.TempDir(), false)
	if err != nil {
		t.Fatalf("empty dir should index cleanly, got %v", err)
	}
	if res.Skipped {
		t.Error("first index of an empty dir should not be skipped")
	}
	if res.Info.Symbols != 0 {
		t.Errorf("empty dir should yield zero symbols, got %d", res.Info.Symbols)
	}
}
