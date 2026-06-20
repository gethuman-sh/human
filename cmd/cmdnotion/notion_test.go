package cmdnotion

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/knowledge/notion"
)

// --- mock notion client ---

type mockNotionClient struct {
	searchFn        func(ctx context.Context, query string) ([]notion.SearchResult, error)
	getPageFn       func(ctx context.Context, pageID string) (string, error)
	queryDatabaseFn func(ctx context.Context, dbID string) ([]notion.DatabaseRow, error)
	listDatabasesFn func(ctx context.Context) ([]notion.DatabaseEntry, error)
}

func (m *mockNotionClient) Search(ctx context.Context, query string) ([]notion.SearchResult, error) {
	return m.searchFn(ctx, query)
}

func (m *mockNotionClient) GetPage(ctx context.Context, pageID string) (string, error) {
	return m.getPageFn(ctx, pageID)
}

func (m *mockNotionClient) QueryDatabase(ctx context.Context, dbID string) ([]notion.DatabaseRow, error) {
	return m.queryDatabaseFn(ctx, dbID)
}

func (m *mockNotionClient) ListDatabases(ctx context.Context) ([]notion.DatabaseEntry, error) {
	return m.listDatabasesFn(ctx)
}

// --- search tests ---

func TestRunNotionSearch_JSON(t *testing.T) {
	results := []notion.SearchResult{
		{ID: "page-1", Title: "Quarterly Report", URL: "https://notion.so/page-1", Type: "page"},
	}
	client := &mockNotionClient{
		searchFn: func(_ context.Context, query string) ([]notion.SearchResult, error) {
			assert.Equal(t, "quarterly", query)
			return results, nil
		},
	}

	var buf bytes.Buffer
	err := runNotionSearch(context.Background(), client, &buf, "quarterly", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"id": "page-1"`)
	assert.Contains(t, buf.String(), `"title": "Quarterly Report"`)
}

func TestRunNotionSearch_Table(t *testing.T) {
	results := []notion.SearchResult{
		{ID: "page-1", Title: "Report", URL: "https://notion.so/page-1", Type: "page"},
	}
	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return results, nil
		},
	}

	var buf bytes.Buffer
	err := runNotionSearch(context.Background(), client, &buf, "report", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "TYPE")
	assert.Contains(t, buf.String(), "TITLE")
	assert.Contains(t, buf.String(), "page-1")
	assert.Contains(t, buf.String(), "Report")
}

func TestRunNotionSearch_Error(t *testing.T) {
	client := &mockNotionClient{
		searchFn: func(_ context.Context, _ string) ([]notion.SearchResult, error) {
			return nil, fmt.Errorf("search failed")
		},
	}

	var buf bytes.Buffer
	err := runNotionSearch(context.Background(), client, &buf, "test", false)
	assert.EqualError(t, err, "search failed")
}

// --- page get tests ---

func TestRunNotionPageGet(t *testing.T) {
	client := &mockNotionClient{
		getPageFn: func(_ context.Context, pageID string) (string, error) {
			assert.Equal(t, "page-1", pageID)
			return "# My Page\n\nContent here\n", nil
		},
	}

	var buf bytes.Buffer
	err := runNotionPageGet(context.Background(), client, &buf, "page-1")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "# My Page")
	assert.Contains(t, buf.String(), "Content here")
}

func TestRunNotionPageGet_Error(t *testing.T) {
	client := &mockNotionClient{
		getPageFn: func(_ context.Context, _ string) (string, error) {
			return "", fmt.Errorf("page not found")
		},
	}

	var buf bytes.Buffer
	err := runNotionPageGet(context.Background(), client, &buf, "bad-id")
	assert.EqualError(t, err, "page not found")
}

// --- database query tests ---

func TestRunNotionDatabaseQuery_JSON(t *testing.T) {
	rows := []notion.DatabaseRow{
		{ID: "row-1", URL: "https://notion.so/row-1", Properties: map[string]string{"Name": "Task", "Status": "Done"}},
	}
	client := &mockNotionClient{
		queryDatabaseFn: func(_ context.Context, dbID string) ([]notion.DatabaseRow, error) {
			assert.Equal(t, "db-1", dbID)
			return rows, nil
		},
	}

	var buf bytes.Buffer
	err := runNotionDatabaseQuery(context.Background(), client, &buf, "db-1", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"id": "row-1"`)
	assert.Contains(t, buf.String(), `"Name": "Task"`)
}

func TestRunNotionDatabaseQuery_Table(t *testing.T) {
	rows := []notion.DatabaseRow{
		{ID: "row-1", URL: "https://notion.so/row-1", Properties: map[string]string{"Name": "Task"}},
	}
	client := &mockNotionClient{
		queryDatabaseFn: func(_ context.Context, _ string) ([]notion.DatabaseRow, error) {
			return rows, nil
		},
	}

	var buf bytes.Buffer
	err := runNotionDatabaseQuery(context.Background(), client, &buf, "db-1", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "Name")
	assert.Contains(t, buf.String(), "row-1")
	assert.Contains(t, buf.String(), "Task")
}

func TestRunNotionDatabaseQuery_Error(t *testing.T) {
	client := &mockNotionClient{
		queryDatabaseFn: func(_ context.Context, _ string) ([]notion.DatabaseRow, error) {
			return nil, fmt.Errorf("query failed")
		},
	}

	var buf bytes.Buffer
	err := runNotionDatabaseQuery(context.Background(), client, &buf, "db-1", false)
	assert.EqualError(t, err, "query failed")
}

// --- databases list tests ---

func TestRunNotionDatabasesList_JSON(t *testing.T) {
	entries := []notion.DatabaseEntry{
		{ID: "db-1", Title: "Tasks", URL: "https://notion.so/db-1"},
	}
	client := &mockNotionClient{
		listDatabasesFn: func(_ context.Context) ([]notion.DatabaseEntry, error) {
			return entries, nil
		},
	}

	var buf bytes.Buffer
	err := runNotionDatabasesList(context.Background(), client, &buf, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"id": "db-1"`)
	assert.Contains(t, buf.String(), `"title": "Tasks"`)
}

func TestRunNotionDatabasesList_Table(t *testing.T) {
	entries := []notion.DatabaseEntry{
		{ID: "db-1", Title: "Tasks", URL: "https://notion.so/db-1"},
	}
	client := &mockNotionClient{
		listDatabasesFn: func(_ context.Context) ([]notion.DatabaseEntry, error) {
			return entries, nil
		},
	}

	var buf bytes.Buffer
	err := runNotionDatabasesList(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "TITLE")
	assert.Contains(t, buf.String(), "db-1")
	assert.Contains(t, buf.String(), "Tasks")
}

func TestRunNotionDatabasesList_Error(t *testing.T) {
	client := &mockNotionClient{
		listDatabasesFn: func(_ context.Context) ([]notion.DatabaseEntry, error) {
			return nil, fmt.Errorf("list failed")
		},
	}

	var buf bytes.Buffer
	err := runNotionDatabasesList(context.Background(), client, &buf, false)
	assert.EqualError(t, err, "list failed")
}

// --- print function tests ---

func TestPrintSearchJSON_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printSearchJSON(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "null")
}

func TestPrintSearchTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printSearchTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No results found")
}

func TestPrintDatabasesTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printDatabasesTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No databases found")
}

func TestPrintDatabaseRowsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printDatabaseRowsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No rows found")
}

// --- command tree tests ---

func TestNotionCmd_hasSubcommands(t *testing.T) {
	cmd := BuildNotionCommands()
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["search QUERY"], "expected 'search' subcommand")
	assert.True(t, subNames["page"], "expected 'page' subcommand")
	assert.True(t, subNames["database"], "expected 'database' subcommand")
	assert.True(t, subNames["databases"], "expected 'databases' subcommand")
}
