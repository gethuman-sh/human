package cmdindex

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/index"
	"github.com/gethuman-sh/human/internal/knowledge/notion"
	"github.com/gethuman-sh/human/internal/tracker"
)

// testDeps returns IndexDeps with an in-memory store.
func testDeps(t *testing.T) (IndexDeps, *index.SQLiteStore) {
	t.Helper()
	store, err := index.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deps := IndexDeps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return nil, nil
		},
		LoadNotionInstances: func(_ string) ([]index.NotionInstance, error) {
			return nil, nil
		},
		DBPath: func() string { return ":memory:" },
		NewStore: func(_ string) (index.Store, error) {
			return store, nil
		},
	}
	return deps, store
}

func seedStore(t *testing.T, store *index.SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "KAN-42", Source: "work", Kind: "jira", Project: "KAN",
		Title: "Implement retry logic", Status: "In Progress", Assignee: "alice",
	}, "webhook delivery retry mechanism"))
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "ENG-7", Source: "eng", Kind: "linear", Project: "ENG",
		Title: "Fix login page", Status: "Open", Assignee: "bob",
	}, "OAuth2 login flow broken on mobile"))
}

func TestRunSearch_agentOutput(t *testing.T) {
	deps, store := testDeps(t)
	seedStore(t, store)

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "retry", 10, "", false, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "KAN-42: Implement retry logic") {
		t.Errorf("expected KAN-42 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "human get KAN-42") {
		t.Errorf("expected 'human get KAN-42' hint, got:\n%s", out)
	}
}

func TestRunSearch_jsonOutput(t *testing.T) {
	deps, store := testDeps(t)
	seedStore(t, store)

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "retry", 10, "", true, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	var entries []index.Entry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}
	if len(entries) != 1 || entries[0].Key != "KAN-42" {
		t.Errorf("expected [KAN-42], got %v", entries)
	}
}

func TestRunSearch_tableOutput(t *testing.T) {
	deps, store := testDeps(t)
	seedStore(t, store)

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "retry", 10, "", false, true, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "KEY") || !strings.Contains(out, "TITLE") {
		t.Errorf("expected table headers, got:\n%s", out)
	}
	if !strings.Contains(out, "KAN-42") {
		t.Errorf("expected KAN-42 in table, got:\n%s", out)
	}
}

func TestRunSearch_noResults(t *testing.T) {
	deps, store := testDeps(t)
	seedStore(t, store)

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "nonexistent", 10, "", false, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	if !strings.Contains(buf.String(), "No results found") {
		t.Errorf("expected 'No results found', got:\n%s", buf.String())
	}
}

func TestRunIndex_syncsAllInstances(t *testing.T) {
	deps, _ := testDeps(t)

	provider := &mockProvider{
		listFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: opts.Project + "-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Test"}, nil
		},
	}

	deps.LoadInstances = func(_ string) ([]tracker.Instance, error) {
		return []tracker.Instance{
			{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
		}, nil
	}

	var buf bytes.Buffer
	err := RunIndex(context.Background(), &buf, "", false, deps)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	if !strings.Contains(buf.String(), "1 indexed") {
		t.Errorf("expected '1 indexed', got:\n%s", buf.String())
	}
}

func TestRunIndex_filtersSource(t *testing.T) {
	deps, _ := testDeps(t)

	jiraProvider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Jira issue"}, nil
		},
	}
	linearProvider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			t.Fatal("linear should not be called when filtering by jira")
			return nil, nil
		},
	}

	deps.LoadInstances = func(_ string) ([]tracker.Instance, error) {
		return []tracker.Instance{
			{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: jiraProvider},
			{Name: "eng", Kind: "linear", Projects: []string{"ENG"}, Provider: linearProvider},
		}, nil
	}

	var buf bytes.Buffer
	err := RunIndex(context.Background(), &buf, "jira", false, deps)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
}

func TestRunIndexStatus_showsStats(t *testing.T) {
	deps, store := testDeps(t)
	seedStore(t, store)

	var buf bytes.Buffer
	err := RunIndexStatus(context.Background(), &buf, deps)
	if err != nil {
		t.Fatalf("RunIndexStatus: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Total entries: 2") {
		t.Errorf("expected 'Total entries: 2', got:\n%s", out)
	}
	if !strings.Contains(out, "jira") {
		t.Errorf("expected 'jira' in stats, got:\n%s", out)
	}
}

// --- Notion integration tests ---

// mockNotionClient implements index.NotionClient for testing.
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

func TestRunIndex_syncsNotionInstances(t *testing.T) {
	deps, _ := testDeps(t)

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "page-1", Title: "Auth Spec", URL: "https://notion.so/page-1", Type: "page"},
			}, nil
		},
		getPageFn: func(_ context.Context, _ string) (string, error) {
			return "# Auth Spec content", nil
		},
	}

	deps.LoadNotionInstances = func(_ string) ([]index.NotionInstance, error) {
		return []index.NotionInstance{
			{Name: "workspace", URL: "https://api.notion.com", Client: client},
		}, nil
	}

	var buf bytes.Buffer
	err := RunIndex(context.Background(), &buf, "", false, deps)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "1 pages") {
		t.Errorf("expected '1 pages' in output, got:\n%s", out)
	}
}

func TestRunIndex_filtersNotionSource(t *testing.T) {
	deps, _ := testDeps(t)

	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return []notion.SearchResult{
				{ID: "page-1", Title: "Auth Spec", URL: "https://notion.so/page-1", Type: "page"},
			}, nil
		},
		getPageFn: func(_ context.Context, _ string) (string, error) {
			return "# Content", nil
		},
	}

	deps.LoadNotionInstances = func(_ string) ([]index.NotionInstance, error) {
		return []index.NotionInstance{
			{Name: "workspace", URL: "https://api.notion.com", Client: client},
		}, nil
	}

	// Should not call tracker ListIssues.
	deps.LoadInstances = func(_ string) ([]tracker.Instance, error) {
		return []tracker.Instance{
			{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: &mockProvider{
				listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
					t.Fatal("jira should not be called when source=notion")
					return nil, nil
				},
			}},
		}, nil
	}

	var buf bytes.Buffer
	err := RunIndex(context.Background(), &buf, "notion", false, deps)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "1 pages") {
		t.Errorf("expected '1 pages' in output, got:\n%s", out)
	}
}

func TestRunIndex_skipsNotionForOtherSource(t *testing.T) {
	deps, _ := testDeps(t)

	deps.LoadNotionInstances = func(_ string) ([]index.NotionInstance, error) {
		t.Fatal("LoadNotionInstances should not be called when source=jira")
		return nil, nil
	}

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Test"}, nil
		},
	}

	deps.LoadInstances = func(_ string) ([]tracker.Instance, error) {
		return []tracker.Instance{
			{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
		}, nil
	}

	var buf bytes.Buffer
	err := RunIndex(context.Background(), &buf, "jira", false, deps)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
}

func TestRunSearch_notionPageOutput(t *testing.T) {
	deps, store := testDeps(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "abc123", Source: "workspace", Kind: "notion", Project: "workspace",
		Title: "Auth Spec", Status: "page",
	}, "authentication specification content"))

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "Auth Spec", 10, "", false, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Auth Spec") {
		t.Errorf("expected title in output, got:\n%s", out)
	}
	if !strings.Contains(out, "human notion page get abc123") {
		t.Errorf("expected notion page get hint, got:\n%s", out)
	}
}

func TestRunSearch_notionDatabaseOutput(t *testing.T) {
	deps, store := testDeps(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "db456", Source: "workspace", Kind: "notion", Project: "workspace",
		Title: "Q1 Roadmap", Status: "database",
	}, "Name: Auth | Status: Done"))

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "Roadmap", 10, "", false, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Q1 Roadmap") {
		t.Errorf("expected title in output, got:\n%s", out)
	}
	if !strings.Contains(out, "human notion database query db456") {
		t.Errorf("expected notion database query hint, got:\n%s", out)
	}
}

func TestRunSearch_sourceFilter(t *testing.T) {
	deps, store := testDeps(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "KAN-1", Source: "work", Kind: "jira", Project: "KAN",
		Title: "Jira issue about auth", Status: "Open",
	}, "auth flow"))
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "page-1", Source: "workspace", Kind: "notion", Project: "workspace",
		Title: "Notion auth spec", Status: "page",
	}, "auth specification"))

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "auth", 10, "notion", false, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Notion auth spec") {
		t.Errorf("expected notion result, got:\n%s", out)
	}
	if strings.Contains(out, "KAN-1") {
		t.Errorf("did not expect jira result when filtering by notion, got:\n%s", out)
	}
}

func TestRunSearch_mixedResults(t *testing.T) {
	deps, store := testDeps(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "KAN-1", Source: "work", Kind: "jira", Project: "KAN",
		Title: "Jira auth issue", Status: "Open",
	}, "auth"))
	require.NoError(t, store.UpsertEntry(ctx, index.Entry{
		Key: "page-1", Source: "workspace", Kind: "notion", Project: "workspace",
		Title: "Notion auth spec", Status: "page",
	}, "auth specification"))

	var buf bytes.Buffer
	err := RunSearch(context.Background(), &buf, "auth", 10, "", false, false, deps)
	if err != nil {
		t.Fatalf("RunSearch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "KAN-1") {
		t.Errorf("expected jira result, got:\n%s", out)
	}
	if !strings.Contains(out, "Notion auth spec") {
		t.Errorf("expected notion result, got:\n%s", out)
	}
	// Notion result should use title-first format (no "page-1:" prefix).
	if strings.Contains(out, "page-1:") {
		t.Errorf("notion result should not use key:title format, got:\n%s", out)
	}
}

// mockProvider implements tracker.Provider for testing.
type mockProvider struct {
	listFn func(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error)
	getFn  func(ctx context.Context, key string) (*tracker.Issue, error)
}

func (m *mockProvider) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	return m.listFn(ctx, opts)
}
func (m *mockProvider) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	return m.getFn(ctx, key)
}
func (m *mockProvider) CreateIssue(_ context.Context, _ *tracker.Issue) (*tracker.Issue, error) {
	return nil, nil
}
func (m *mockProvider) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	return nil, nil
}
func (m *mockProvider) AddComment(_ context.Context, _, _ string) (*tracker.Comment, error) {
	return nil, nil
}
func (m *mockProvider) DeleteIssue(_ context.Context, _ string) error { return nil }
func (m *mockProvider) TransitionIssue(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockProvider) AssignIssue(_ context.Context, _, _ string) error { return nil }
func (m *mockProvider) GetCurrentUser(_ context.Context) (string, error) {
	return "", nil
}
func (m *mockProvider) EditIssue(_ context.Context, _ string, _ tracker.EditOptions) (*tracker.Issue, error) {
	return nil, nil
}
func (m *mockProvider) ListStatuses(_ context.Context, _ string) ([]tracker.Status, error) {
	return nil, nil
}
