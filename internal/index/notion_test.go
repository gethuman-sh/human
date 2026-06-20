package index

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/knowledge/notion"
)

// mockNotionClient implements NotionClient for testing.
type mockNotionClient struct {
	searchFn        func(ctx context.Context, query string) ([]notion.SearchResult, error)
	getPageFn       func(ctx context.Context, pageID string) (string, error)
	listDatabasesFn func(ctx context.Context) ([]notion.DatabaseEntry, error)
	queryDatabaseFn func(ctx context.Context, dbID string) ([]notion.DatabaseRow, error)
}

func (m *mockNotionClient) Search(ctx context.Context, query string) ([]notion.SearchResult, error) {
	return m.searchFn(ctx, query)
}

func (m *mockNotionClient) GetPage(ctx context.Context, pageID string) (string, error) {
	return m.getPageFn(ctx, pageID)
}

func (m *mockNotionClient) ListDatabases(ctx context.Context) ([]notion.DatabaseEntry, error) {
	return m.listDatabasesFn(ctx)
}

func (m *mockNotionClient) QueryDatabase(ctx context.Context, dbID string) ([]notion.DatabaseRow, error) {
	return m.queryDatabaseFn(ctx, dbID)
}

func TestSyncNotion_indexesPages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "page-1", Title: "Auth Spec", URL: "https://notion.so/page-1", Type: "page"},
				{ID: "page-2", Title: "Design Doc", URL: "https://notion.so/page-2", Type: "page"},
			}, nil
		},
		getPageFn: func(_ context.Context, pageID string) (string, error) {
			return "# Content of " + pageID, nil
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, err := SyncNotion(ctx, s, instances, &buf)
	if err != nil {
		t.Fatalf("SyncNotion: %v", err)
	}
	if result.Pages != 2 {
		t.Errorf("expected 2 pages, got %d", result.Pages)
	}
	if result.Errors != 0 {
		t.Errorf("expected 0 errors, got %d", result.Errors)
	}

	keys, _ := s.AllKeys(ctx, "workspace")
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}

	// Verify entries are searchable.
	entries, _ := s.Search(ctx, "Auth Spec", 10)
	if len(entries) != 1 || entries[0].Key != "page-1" {
		t.Errorf("expected search to find page-1, got %v", entries)
	}
}

func TestSyncNotion_indexesDatabases(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "db-1", Title: "Q1 Roadmap", URL: "https://notion.so/db-1", Type: "database"},
			}, nil
		},
		queryDatabaseFn: func(_ context.Context, _ string) ([]notion.DatabaseRow, error) {
			return []notion.DatabaseRow{
				{ID: "row-1", URL: "https://notion.so/row-1", Properties: map[string]string{"Name": "Auth Spec", "Status": "Done", "Owner": "Alice"}},
				{ID: "row-2", URL: "https://notion.so/row-2", Properties: map[string]string{"Name": "Search", "Status": "In Progress"}},
			}, nil
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, err := SyncNotion(ctx, s, instances, &buf)
	if err != nil {
		t.Fatalf("SyncNotion: %v", err)
	}
	if result.Databases != 1 {
		t.Errorf("expected 1 database, got %d", result.Databases)
	}

	// Verify flattened properties are searchable.
	entries, _ := s.Search(ctx, "Alice", 10)
	if len(entries) != 1 || entries[0].Key != "db-1" {
		t.Errorf("expected search to find db-1, got %v", entries)
	}
}

func TestSyncNotion_prunesStaleEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	// Pre-populate a stale entry.
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "stale-page", Source: "workspace", Kind: "notion"}, "old content"))

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "page-1", Title: "Current", URL: "https://notion.so/page-1", Type: "page"},
			}, nil
		},
		getPageFn: func(_ context.Context, _ string) (string, error) {
			return "# Current", nil
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, _ := SyncNotion(ctx, s, instances, &buf)
	if result.Pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", result.Pruned)
	}

	keys, _ := s.AllKeys(ctx, "workspace")
	if len(keys) != 1 || keys[0] != "page-1" {
		t.Errorf("expected [page-1], got %v", keys)
	}
}

func TestSyncNotion_handlesGetPageError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "page-1", Title: "Fails", URL: "https://notion.so/page-1", Type: "page"},
				{ID: "page-2", Title: "Works", URL: "https://notion.so/page-2", Type: "page"},
			}, nil
		},
		getPageFn: func(_ context.Context, pageID string) (string, error) {
			if pageID == "page-1" {
				return "", context.DeadlineExceeded
			}
			return "# OK", nil
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, _ := SyncNotion(ctx, s, instances, &buf)
	if result.Errors != 1 {
		t.Errorf("expected 1 error, got %d", result.Errors)
	}
	if result.Pages != 1 {
		t.Errorf("expected 1 page indexed, got %d", result.Pages)
	}
}

func TestSyncNotion_handlesQueryDatabaseError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "db-1", Title: "Fails", URL: "https://notion.so/db-1", Type: "database"},
			}, nil
		},
		queryDatabaseFn: func(_ context.Context, _ string) ([]notion.DatabaseRow, error) {
			return nil, context.DeadlineExceeded
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, _ := SyncNotion(ctx, s, instances, &buf)
	if result.Errors != 1 {
		t.Errorf("expected 1 error, got %d", result.Errors)
	}
	if result.Databases != 0 {
		t.Errorf("expected 0 databases, got %d", result.Databases)
	}
}

func TestSyncNotion_handlesSearchError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return nil, context.DeadlineExceeded
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, _ := SyncNotion(ctx, s, instances, &buf)
	if result.Errors != 1 {
		t.Errorf("expected 1 error, got %d", result.Errors)
	}
}

func TestSyncNotion_emptyWorkspace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return nil, nil
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, err := SyncNotion(ctx, s, instances, &buf)
	if err != nil {
		t.Fatalf("SyncNotion: %v", err)
	}
	if result.Pages != 0 || result.Databases != 0 || result.Errors != 0 {
		t.Errorf("expected all zeros, got %+v", result)
	}
}

func TestSyncNotion_emptyInstances(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	result, err := SyncNotion(ctx, s, nil, &buf)
	if err != nil {
		t.Fatalf("SyncNotion: %v", err)
	}
	if result.Pages != 0 || result.Databases != 0 || result.Pruned != 0 || result.Errors != 0 {
		t.Errorf("expected all zeros, got %+v", result)
	}
}

func TestSyncNotion_unknownTypeIgnored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "page-1", Title: "Real page", URL: "https://notion.so/page-1", Type: "page"},
				{ID: "unknown-1", Title: "Unknown type", URL: "https://notion.so/unknown-1", Type: "comment"}, // unknown type
			}, nil
		},
		getPageFn: func(_ context.Context, pageID string) (string, error) {
			return "# Content of " + pageID, nil
		},
	}

	instances := []NotionInstance{
		{Name: "workspace", URL: "https://api.notion.com", Client: client},
	}

	result, err := SyncNotion(ctx, s, instances, &buf)
	if err != nil {
		t.Fatalf("SyncNotion: %v", err)
	}
	if result.Pages != 1 {
		t.Errorf("expected 1 page, got %d", result.Pages)
	}
	if result.Databases != 0 {
		t.Errorf("expected 0 databases, got %d", result.Databases)
	}
}

func TestFlattenProperties(t *testing.T) {
	props := map[string]string{
		"Name":   "Auth Spec",
		"Status": "Done",
		"Owner":  "Alice",
	}
	got := flattenProperties(props)
	expected := "Name: Auth Spec | Owner: Alice | Status: Done"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestFlattenProperties_skipsEmpty(t *testing.T) {
	props := map[string]string{
		"Name":   "Test",
		"Status": "",
	}
	got := flattenProperties(props)
	expected := "Name: Test"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
