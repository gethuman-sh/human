package notion

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoRequest_setsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ntn_secret", r.Header.Get("Authorization"))
		assert.Equal(t, "2022-06-28", r.Header.Get("Notion-Version"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_, _ = fmt.Fprint(w, `{"object":"list","results":[],"has_more":false}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_secret")
	resp, err := client.doRequest(context.Background(), http.MethodPost, "/v1/search", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDoRequest_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"code":"unauthorized"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_bad")
	_, err := client.doRequest(context.Background(), http.MethodPost, "/v1/search", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 401")
}

func TestSearch_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/search", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"results": [
				{
					"object": "page",
					"id": "page-1",
					"url": "https://notion.so/page-1",
					"properties": {
						"Name": {"type": "title", "title": [{"plain_text": "My Page"}]}
					}
				},
				{
					"object": "database",
					"id": "db-1",
					"url": "https://notion.so/db-1",
					"title": [{"plain_text": "My Database"}]
				}
			],
			"has_more": false
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	results, err := client.Search(context.Background(), "test")
	require.NoError(t, err)
	require.Len(t, results, 2)

	assert.Equal(t, "page-1", results[0].ID)
	assert.Equal(t, "My Page", results[0].Title)
	assert.Equal(t, "page", results[0].Type)

	assert.Equal(t, "db-1", results[1].ID)
	assert.Equal(t, "My Database", results[1].Title)
	assert.Equal(t, "database", results[1].Type)
}

func TestSearch_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"object":"list","results":[],"has_more":false}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	results, err := client.Search(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestGetPage_happy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pages/page-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"object": "page",
			"id": "page-1",
			"url": "https://notion.so/page-1",
			"properties": {
				"Name": {"type": "title", "title": [{"plain_text": "Test Page"}]}
			}
		}`)
	})
	mux.HandleFunc("/v1/blocks/page-1/children", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"results": [
				{
					"object": "block",
					"id": "b1",
					"type": "paragraph",
					"has_children": false,
					"paragraph": {
						"rich_text": [{"type": "text", "text": {"content": "Hello"}, "annotations": {}, "plain_text": "Hello"}]
					}
				}
			],
			"has_more": false
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	md, err := client.GetPage(context.Background(), "page-1")
	require.NoError(t, err)
	assert.Contains(t, md, "# Test Page")
	assert.Contains(t, md, "Hello")
}

func TestGetPage_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	_, err := client.GetPage(context.Background(), "bad-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 404")
}

func TestQueryDatabase_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/databases/db-1/query", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"results": [
				{
					"object": "page",
					"id": "row-1",
					"url": "https://notion.so/row-1",
					"properties": {
						"Name": {"type": "title", "title": [{"plain_text": "Row One"}]},
						"Status": {"type": "select", "select": {"name": "Done"}}
					}
				}
			],
			"has_more": false
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	rows, err := client.QueryDatabase(context.Background(), "db-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)

	assert.Equal(t, "row-1", rows[0].ID)
	assert.Equal(t, "Row One", rows[0].Properties["Name"])
	assert.Equal(t, "Done", rows[0].Properties["Status"])
}

func TestQueryDatabase_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	_, err := client.QueryDatabase(context.Background(), "db-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 403")
}

func TestListDatabases_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/search", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"results": [
				{
					"object": "database",
					"id": "db-1",
					"url": "https://notion.so/db-1",
					"title": [{"plain_text": "Tasks"}]
				},
				{
					"object": "database",
					"id": "db-2",
					"url": "https://notion.so/db-2",
					"title": [{"plain_text": "Projects"}]
				}
			],
			"has_more": false
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	entries, err := client.ListDatabases(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "db-1", entries[0].ID)
	assert.Equal(t, "Tasks", entries[0].Title)
	assert.Equal(t, "db-2", entries[1].ID)
	assert.Equal(t, "Projects", entries[1].Title)
}

func TestListDatabases_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"object":"list","results":[],"has_more":false}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	entries, err := client.ListDatabases(context.Background())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestGetBlockChildren_pagination(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"object": "list",
				"results": [
					{"object":"block","id":"b1","type":"paragraph","has_children":false,
					 "paragraph":{"rich_text":[{"type":"text","text":{"content":"First"},"annotations":{},"plain_text":"First"}]}}
				],
				"has_more": true,
				"next_cursor": "cursor-2"
			}`)
		} else {
			assert.Contains(t, r.URL.RawQuery, "start_cursor=cursor-2")
			_, _ = fmt.Fprint(w, `{
				"object": "list",
				"results": [
					{"object":"block","id":"b2","type":"paragraph","has_children":false,
					 "paragraph":{"rich_text":[{"type":"text","text":{"content":"Second"},"annotations":{},"plain_text":"Second"}]}}
				],
				"has_more": false
			}`)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	blocks, err := client.getBlockChildren(context.Background(), "page-1", 0)
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	assert.Equal(t, "b1", blocks[0].ID)
	assert.Equal(t, "b2", blocks[1].ID)
}

func TestGetBlockChildren_recursiveNested(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/blocks/parent/children", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"results": [
				{"object":"block","id":"child-1","type":"paragraph","has_children":true,
				 "paragraph":{"rich_text":[{"type":"text","text":{"content":"Parent"},"annotations":{},"plain_text":"Parent"}]}}
			],
			"has_more": false
		}`)
	})
	mux.HandleFunc("/v1/blocks/child-1/children", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"results": [
				{"object":"block","id":"grandchild","type":"paragraph","has_children":false,
				 "paragraph":{"rich_text":[{"type":"text","text":{"content":"Nested"},"annotations":{},"plain_text":"Nested"}]}}
			],
			"has_more": false
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := New(srv.URL, "ntn_test")
	blocks, err := client.getBlockChildren(context.Background(), "parent", 0)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Len(t, blocks[0].Children, 1)
	assert.Equal(t, "grandchild", blocks[0].Children[0].ID)
}

func TestPropertyToString(t *testing.T) {
	num := 42.0
	url := "https://example.com"
	checked := true
	unchecked := false
	email := "test@example.com"
	phone := "+1234567890"

	tests := []struct {
		name string
		prop notionProperty
		want string
	}{
		{
			name: "title",
			prop: notionProperty{Type: "title", Title: []notionRichText{{PlainText: "Hello"}}},
			want: "Hello",
		},
		{
			name: "rich_text",
			prop: notionProperty{Type: "rich_text", RichText: []notionRichText{{PlainText: "World"}}},
			want: "World",
		},
		{
			name: "number",
			prop: notionProperty{Type: "number", Number: &num},
			want: "42",
		},
		{
			name: "select",
			prop: notionProperty{Type: "select", Select: &selectOption{Name: "High"}},
			want: "High",
		},
		{
			name: "status",
			prop: notionProperty{Type: "status", Status: &selectOption{Name: "In Progress"}},
			want: "In Progress",
		},
		{
			name: "url",
			prop: notionProperty{Type: "url", URL: &url},
			want: "https://example.com",
		},
		{
			name: "checkbox true",
			prop: notionProperty{Type: "checkbox", Checkbox: &checked},
			want: "true",
		},
		{
			name: "checkbox false",
			prop: notionProperty{Type: "checkbox", Checkbox: &unchecked},
			want: "false",
		},
		{
			name: "date with end",
			prop: notionProperty{Type: "date", Date: &dateValue{Start: "2025-01-01", End: "2025-01-31"}},
			want: "2025-01-01 - 2025-01-31",
		},
		{
			name: "date without end",
			prop: notionProperty{Type: "date", Date: &dateValue{Start: "2025-01-01"}},
			want: "2025-01-01",
		},
		{
			name: "email",
			prop: notionProperty{Type: "email", Email: &email},
			want: "test@example.com",
		},
		{
			name: "phone",
			prop: notionProperty{Type: "phone_number", Phone: &phone},
			want: "+1234567890",
		},
		{
			name: "nil number",
			prop: notionProperty{Type: "number"},
			want: "",
		},
		{
			name: "nil select",
			prop: notionProperty{Type: "select"},
			want: "",
		},
		{
			name: "unknown type",
			prop: notionProperty{Type: "formula"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, propertyToString(tt.prop))
		})
	}
}

func TestExtractTitle_page(t *testing.T) {
	props := map[string]notionProperty{
		"Name": {Type: "title", Title: []notionRichText{{PlainText: "Page Title"}}},
	}
	assert.Equal(t, "Page Title", extractTitle("page", props, nil))
}

func TestExtractTitle_database(t *testing.T) {
	dbTitle := []notionRichText{{PlainText: "DB Title"}}
	assert.Equal(t, "DB Title", extractTitle("database", nil, dbTitle))
}

func TestExtractTitle_noTitle(t *testing.T) {
	props := map[string]notionProperty{
		"Status": {Type: "select"},
	}
	assert.Equal(t, "", extractTitle("page", props, nil))
}
