package cmdcodenav

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/query"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// indexModule writes the given files as a module rooted at a temp dir, indexes
// them into a fresh SQLite database, and returns the database path so a command
// test can run against it via --db.
func indexModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "codenav.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	scan := index.RepoScan{Project: "cmdfix", Root: root}
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
	_ = st.Close()
	return dbPath
}

func runCodenav(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := BuildCodenavCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestAccountCmd_json(t *testing.T) {
	db := indexModule(t, map[string]string{
		"go.mod": "module example.com/cmdfix\n\ngo 1.21\n",
		"dom.go": `package cmdfix

// Booking is a reservation a customer makes.
type Booking struct {
	ID string
}

// Confirm records a booking.
func Confirm(b Booking) error { return nil }
`,
	})
	out, err := runCodenav(t, "account", "--db", db, "--json")
	if err != nil {
		t.Fatalf("account --json: %v (out: %s)", err, out)
	}
	var acct query.Account
	if err := json.Unmarshal([]byte(out), &acct); err != nil {
		t.Fatalf("account --json did not parse to query.Account: %v\n%s", err, out)
	}
	if len(acct.Nouns) == 0 {
		t.Fatalf("expected >=1 noun, got none: %s", out)
	}
	if nounHas(acct, "Booking") == nil {
		t.Errorf("expected Booking in account, got %v", acct.Nouns)
	}
}

func TestAccountCmd_textEmpty(t *testing.T) {
	// A module of only funcs has no domain types; the text output must be the
	// plain note (and exit 0), never silence.
	db := indexModule(t, map[string]string{
		"go.mod": "module example.com/cmdfix\n\ngo 1.21\n",
		"main.go": `package main

func A() {}
func main() { A() }
`,
	})
	out, err := runCodenav(t, "account", "--db", db)
	if err != nil {
		t.Fatalf("account (text): %v (out: %s)", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty account printed nothing; want a plain note")
	}
	if !strings.Contains(out, "no type declarations") {
		t.Errorf("empty-account text = %q, want the plain no-types note", out)
	}
}

func TestAccountCmd_unindexedErrors(t *testing.T) {
	// A fresh, empty database must yield the actionable "index first" error
	// rather than an empty account — account is a gated query verb.
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	_, err := runCodenav(t, "account", "--db", dbPath)
	if err == nil {
		t.Fatal("account on an un-indexed db returned no error")
	}
	if !strings.Contains(err.Error(), "no repository is indexed") {
		t.Errorf("error = %v, want the actionable index-first message", err)
	}
}

func nounHas(a query.Account, name string) *query.Noun {
	for i := range a.Nouns {
		if a.Nouns[i].Name == name {
			return &a.Nouns[i]
		}
	}
	return nil
}
