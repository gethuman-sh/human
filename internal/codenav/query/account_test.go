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

// indexDomainFixture writes a small multi-package Go module that exercises every
// signal GetAccount weighs — a documented struct named in a route handler
// (Booking), a documented struct with no route (Invoice, the route-boost
// control), an undocumented struct with many references (Ledger, the intent-vs-
// refs control), a documented interface (Store), an unused undocumented alias
// (Unused, which must be dropped), and an infra type under util/ (Helper, which
// must be excluded by path) — then indexes it through the real pipeline.
func indexDomainFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/dom\n\ngo 1.21\n",
		"dom.go": `package dom

import "net/http"

// Booking is a reservation a customer makes for a room.
type Booking struct {
	ID string
}

// Invoice bills a completed booking.
type Invoice struct {
	Total int
}

type Ledger struct {
	rows int
}

// Store persists and retrieves bookings.
type Store interface {
	Save(b Booking) error
}

type Unused int

// handleBooking names Booking in its signature so the route-handler boost fires.
func handleBooking(w http.ResponseWriter, r *http.Request, b Booking) {}

func useInvoice(i Invoice) Invoice { return i }

func useLedger(l Ledger) Ledger       { return l }
func moreLedger(l Ledger) Ledger      { return useLedger(l) }
func evenMoreLedger(l Ledger) Ledger  { return moreLedger(l) }

type router struct{}

func (router) Get(path string, h func(http.ResponseWriter, *http.Request, Booking)) {}

func wire(rt router) { rt.Get("/bookings", handleBooking) }
`,
		"util/helper.go": `package util

// Helper is infra glue that must never surface as a domain noun.
type Helper struct {
	n int
}
`,
	}
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
	t.Cleanup(func() { _ = st.Close() })

	scan := index.RepoScan{Project: "dom", Root: root}
	backends := index.PickFor(scan)
	if len(backends) == 0 {
		t.Fatal("no indexer matched the domain fixture")
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

// nounByName returns the account noun with the given name, or nil.
func nounByName(a *Account, name string) *Noun {
	for i := range a.Nouns {
		if a.Nouns[i].Name == name {
			return &a.Nouns[i]
		}
	}
	return nil
}

// rankOf returns the zero-based position of a named noun in the account, or -1.
func rankOf(a *Account, name string) int {
	for i := range a.Nouns {
		if a.Nouns[i].Name == name {
			return i
		}
	}
	return -1
}

func TestGetAccount_surfacesDomainNoun(t *testing.T) {
	db, repo := indexDomainFixture(t)
	acct, err := GetAccount(db, repo, 24)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	b := nounByName(acct, "Booking")
	if b == nil {
		t.Fatalf("Booking not in account; got %v", acct.Nouns)
	}
	if !strings.Contains(b.Meaning, "reservation") {
		t.Errorf("Booking meaning = %q, want it to carry the recorded intent", b.Meaning)
	}
	if b.Shape == "" {
		t.Error("Booking shape is empty, want the compact signature")
	}
	if b.Kind != "type" {
		t.Errorf("Booking kind = %q, want type", b.Kind)
	}
}

func TestGetAccount_excludesInfraPackage(t *testing.T) {
	db, repo := indexDomainFixture(t)
	acct, err := GetAccount(db, repo, 24)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if n := nounByName(acct, "Helper"); n != nil {
		t.Errorf("Helper (util/ infra) surfaced in the account: %+v", *n)
	}
}

func TestGetAccount_dropsUnusedUndocumentedAlias(t *testing.T) {
	db, repo := indexDomainFixture(t)
	acct, err := GetAccount(db, repo, 24)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if n := nounByName(acct, "Unused"); n != nil {
		t.Errorf("Unused (no doc, no refs, no shape) surfaced: %+v", *n)
	}
}

func TestGetAccount_intentDrivesOrderNotCalls(t *testing.T) {
	db, repo := indexDomainFixture(t)
	acct, err := GetAccount(db, repo, 24)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	// Invoice is documented with few references; Ledger is undocumented with
	// many. Recorded intent, not reference/call count, must rank Invoice higher.
	inv, led := rankOf(acct, "Invoice"), rankOf(acct, "Ledger")
	if inv < 0 || led < 0 {
		t.Fatalf("expected both Invoice and Ledger in account; got %v", acct.Nouns)
	}
	if inv >= led {
		t.Errorf("Invoice (documented) ranked %d, Ledger (undocumented, more refs) ranked %d; intent must win", inv, led)
	}
	// The account is nouns only — a heavily-called func never leaks in.
	for _, n := range acct.Nouns {
		if n.Kind != "type" {
			t.Errorf("account carried a non-type noun %q (kind %q)", n.Name, n.Kind)
		}
	}
}

func TestGetAccount_routeHandlerBoost(t *testing.T) {
	db, repo := indexDomainFixture(t)
	acct, err := GetAccount(db, repo, 24)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	// Booking and Invoice are both documented structs with equal reference
	// counts; only Booking is named in a route handler, so the +route boost must
	// place it ahead of Invoice.
	bk, inv := rankOf(acct, "Booking"), rankOf(acct, "Invoice")
	if bk < 0 || inv < 0 {
		t.Fatalf("expected Booking and Invoice in account; got %v", acct.Nouns)
	}
	if bk >= inv {
		t.Errorf("Booking (route handler) ranked %d, Invoice (no route) ranked %d; route boost must win", bk, inv)
	}
}

func TestGetAccount_emptyReportsPlainly(t *testing.T) {
	// indexFixture indexes a module of only funcs (A→B→C) — no type declarations.
	db, repo := indexFixture(t)
	acct, err := GetAccount(db, repo, 24)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(acct.Nouns) != 0 {
		t.Fatalf("expected no nouns from a func-only module, got %v", acct.Nouns)
	}
	if acct.Note == "" {
		t.Fatal("empty account must carry a plain note, not silence")
	}
	if !strings.Contains(acct.Note, "no type declarations") {
		t.Errorf("note = %q, want it to explain there are no type declarations", acct.Note)
	}
}

func TestGetAccount_defaultLimit(t *testing.T) {
	db, repo := indexDomainFixture(t)
	// A non-positive limit falls back to the page-sized default rather than
	// returning everything or nothing.
	acct, err := GetAccount(db, repo, 0)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(acct.Nouns) == 0 {
		t.Fatal("default-limit account returned no nouns")
	}
}

func TestGetAccount_limitCapsPage(t *testing.T) {
	db, repo := indexDomainFixture(t)
	acct, err := GetAccount(db, repo, 1)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(acct.Nouns) != 1 {
		t.Errorf("limit 1 returned %d nouns, want 1", len(acct.Nouns))
	}
}

func TestGetAccount_unknownRepoReportsPlainly(t *testing.T) {
	db, _ := indexDomainFixture(t)
	acct, err := GetAccount(db, "doesnotexist", 24)
	if err != nil {
		t.Fatalf("GetAccount unknown repo errored: %v", err)
	}
	if len(acct.Nouns) != 0 {
		t.Errorf("unknown repo returned nouns: %v", acct.Nouns)
	}
	if acct.Note == "" {
		t.Error("unknown repo must report plainly, not with an empty silent account")
	}
}

func TestInInfraPackage(t *testing.T) {
	cases := map[string]bool{
		"internal/util/x.go":    true,
		"internal/tracker/x.go": false,
		"errors/x.go":           true,
		"pkg/mocks/y.go":        true,
		"cmd/app/main.go":       false,
		"helper.go":             false, // a bare filename named helper is not an infra dir
	}
	for path, want := range cases {
		if got := inInfraPackage(path); got != want {
			t.Errorf("inInfraPackage(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestWholeWordIn(t *testing.T) {
	hay := "func(w http.ResponseWriter, r *http.Request, b dom.Booking)"
	cases := map[string]bool{
		"Booking": true,
		"Book":    false,
		"king":    false,
		"":        false,
	}
	for name, want := range cases {
		if got := wholeWordIn(hay, name); got != want {
			t.Errorf("wholeWordIn(hay, %q) = %v, want %v", name, got, want)
		}
	}
	// A repeated near-miss followed by a real match must still be found.
	if !wholeWordIn("xBooking yBooking Booking", "Booking") {
		t.Error("wholeWordIn missed a whole-word match after embedded near-misses")
	}
	// Only embedded occurrences must report no match.
	if wholeWordIn("xBookingy", "Booking") {
		t.Error("wholeWordIn matched an embedded-only occurrence")
	}
}

func TestFoldOneLine_degenerateWidth(t *testing.T) {
	if got := foldOneLine("a b  c", 0); got != "a b c" {
		t.Errorf("foldOneLine width 0 = %q, want folded-but-uncapped %q", got, "a b c")
	}
	if got := foldOneLine("a b  c", 100); got != "a b c" {
		t.Errorf("foldOneLine width 100 = %q, want %q", got, "a b c")
	}
	long := strings.Repeat("x", 50)
	got := foldOneLine(long, 10)
	if len([]rune(got)) != 10 {
		t.Errorf("foldOneLine capped length = %d runes, want 10", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("capped foldOneLine = %q, want an ellipsis suffix", got)
	}
}
