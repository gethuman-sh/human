package recall

import (
	"context"
	"time"
)

// Entry holds the metadata for a single indexed issue.
type Entry struct {
	Key       string    `json:"key"`
	Source    string    `json:"source"` // instance name
	Kind      string    `json:"kind"`   // "jira", "github", etc.
	Project   string    `json:"project"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Assignee  string    `json:"assignee"`
	URL       string    `json:"url"` // instance base URL
	IndexedAt time.Time `json:"indexed_at"`
	// Files are the source paths this ticket's plan names. They make overlap a
	// fact rather than a keyword guess: two tickets describing one problem
	// routinely share no words, only the code they will change.
	Files []string `json:"files,omitempty"`
}

// Stats summarises the current state of the index.
type Stats struct {
	TotalEntries  int            `json:"total_entries"`
	LastIndexedAt time.Time      `json:"last_indexed_at"`
	ByKind        map[string]int `json:"by_kind"`
	BySource      map[string]int `json:"by_source"`
}

// Store is the persistence interface for the search index.
type Store interface {
	UpsertEntry(ctx context.Context, entry Entry, description string) error
	DeleteEntry(ctx context.Context, key, source string) error
	// SearchByFile returns entries whose plan names the given source path.
	// Exact, because the point is to answer "who else is changing this file"
	// without the fuzziness of matching a path as free text.
	SearchByFile(ctx context.Context, path string, limit int) ([]Entry, error)
	// Search returns up to limit matching entries across all kinds.
	Search(ctx context.Context, query string, limit int) ([]Entry, error)
	// SearchWithKind returns up to limit matching entries restricted
	// to a single kind ("notion", "jira", ...). Filtering happens in
	// the SQL engine so clients cannot observe empty results when the
	// top-ranked hits belong to another kind.
	SearchWithKind(ctx context.Context, query, kind string, limit int) ([]Entry, error)
	Stats(ctx context.Context) (*Stats, error)
	AllKeys(ctx context.Context, source string) ([]string, error)
	LastIndexedAt(ctx context.Context, source string) (time.Time, error)
	Close() error
}
