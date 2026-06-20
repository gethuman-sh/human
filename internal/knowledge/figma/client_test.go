package figma

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
		assert.Equal(t, "figd_secret", r.Header.Get("X-Figma-Token"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Empty(t, r.Header.Get("Authorization")) // Figma uses X-Figma-Token, not Bearer
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_secret")
	resp, err := client.doRequest(context.Background(), http.MethodGet, "/v1/files/abc", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDoRequest_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"status":403,"err":"Invalid token"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_bad")
	_, err := client.doRequest(context.Background(), http.MethodGet, "/v1/files/abc", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 401")
}

func TestGetFile_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/files/abc123", r.URL.Path)
		assert.Equal(t, "depth=1", r.URL.RawQuery)
		_, _ = fmt.Fprint(w, `{
			"name": "My Design",
			"lastModified": "2025-01-15T10:30:00Z",
			"thumbnailUrl": "https://figma.com/thumb/abc",
			"version": "123456",
			"document": {
				"id": "0:0",
				"name": "Document",
				"type": "DOCUMENT",
				"children": [
					{"id": "0:1", "name": "Page 1", "type": "CANVAS", "children": [{"id":"1:1"},{"id":"1:2"}]},
					{"id": "0:2", "name": "Page 2", "type": "CANVAS", "children": [{"id":"2:1"}]}
				]
			},
			"components": {"comp:1": {"key": "k1", "name": "Button"}}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	summary, err := client.GetFile(context.Background(), "abc123")
	require.NoError(t, err)

	assert.Equal(t, "My Design", summary.Name)
	assert.Equal(t, "2025-01-15T10:30:00Z", summary.LastModified)
	assert.Equal(t, "123456", summary.Version)
	assert.Equal(t, 1, summary.ComponentCount)
	require.Len(t, summary.Pages, 2)
	assert.Equal(t, "Page 1", summary.Pages[0].Name)
	assert.Equal(t, 2, summary.Pages[0].ChildCount)
	assert.Equal(t, "Page 2", summary.Pages[1].Name)
	assert.Equal(t, 1, summary.Pages[1].ChildCount)
}

func TestGetFile_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	_, err := client.GetFile(context.Background(), "bad-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 404")
}

func TestGetNodes_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/v1/files/abc/nodes")
		_, _ = fmt.Fprint(w, `{
			"nodes": {
				"0:1": {
					"document": {
						"id": "0:1",
						"name": "Header",
						"type": "FRAME",
						"absoluteBoundingBox": {"x": 0, "y": 0, "width": 1440, "height": 80}
					}
				}
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	nodes, err := client.GetNodes(context.Background(), "abc", []string{"0:1"})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "Header", nodes[0].Name)
	assert.Equal(t, 1440.0, nodes[0].Size.Width)
}

func TestGetNodes_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	_, err := client.GetNodes(context.Background(), "abc", []string{"0:1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 403")
}

func TestGetFileComponents_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/files/abc/components", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"meta": {
				"components": [
					{
						"key": "k1",
						"node_id": "1:1",
						"name": "Button",
						"description": "Primary button",
						"containing_frame": {
							"name": "Components",
							"nodeId": "0:1",
							"pageId": "0:0",
							"pageName": "Design System"
						}
					}
				]
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	components, err := client.GetFileComponents(context.Background(), "abc")
	require.NoError(t, err)
	require.Len(t, components, 1)
	assert.Equal(t, "Button", components[0].Name)
	assert.Equal(t, "Primary button", components[0].Description)
	assert.Equal(t, "Design System", components[0].Page)
	assert.Equal(t, "Components", components[0].Frame)
}

func TestGetFileComponents_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"meta": {"components": []}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	components, err := client.GetFileComponents(context.Background(), "abc")
	require.NoError(t, err)
	assert.Empty(t, components)
}

func TestGetFileComments_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/files/abc/comments", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"comments": [
				{
					"id": "c1",
					"message": "Looks good!",
					"created_at": "2025-01-15T10:30:00Z",
					"resolved_at": null,
					"user": {"handle": "Alice", "img_url": "https://img/alice"},
					"client_meta": {"node_id": "1:1"},
					"parent_id": ""
				},
				{
					"id": "c2",
					"message": "Fixed",
					"created_at": "2025-01-16T10:30:00Z",
					"resolved_at": "2025-01-17T10:30:00Z",
					"user": {"handle": "Bob", "img_url": ""},
					"parent_id": "c1"
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	comments, err := client.GetFileComments(context.Background(), "abc")
	require.NoError(t, err)
	require.Len(t, comments, 2)

	assert.Equal(t, "c1", comments[0].ID)
	assert.Equal(t, "Alice", comments[0].Author)
	assert.Equal(t, "Looks good!", comments[0].Message)
	assert.False(t, comments[0].Resolved)
	assert.Equal(t, "1:1", comments[0].NodeID)

	assert.Equal(t, "c2", comments[1].ID)
	assert.Equal(t, "Bob", comments[1].Author)
	assert.True(t, comments[1].Resolved)
	assert.Equal(t, "c1", comments[1].ParentID)
}

func TestGetFileComments_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"comments": []}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	comments, err := client.GetFileComments(context.Background(), "abc")
	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestExportImages_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v1/images/abc")
		assert.Contains(t, r.URL.RawQuery, "format=png")
		_, _ = fmt.Fprint(w, `{
			"images": {
				"0:1": "https://figma-alpha-api.s3.us-west-2.amazonaws.com/images/abc123"
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	exports, err := client.ExportImages(context.Background(), "abc", []string{"0:1"}, "png")
	require.NoError(t, err)
	require.Len(t, exports, 1)
	assert.Equal(t, "0:1", exports[0].NodeID)
	assert.Contains(t, exports[0].URL, "figma-alpha-api")
}

func TestExportImages_defaultFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "format=png")
		_, _ = fmt.Fprint(w, `{"images": {"0:1": "https://example.com/img.png"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	exports, err := client.ExportImages(context.Background(), "abc", []string{"0:1"}, "")
	require.NoError(t, err)
	require.Len(t, exports, 1)
}

func TestListProjects_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/teams/team123/projects", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"projects": [
				{"id": 1, "name": "Mobile App"},
				{"id": 2, "name": "Web App"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	projects, err := client.ListProjects(context.Background(), "team123")
	require.NoError(t, err)
	require.Len(t, projects, 2)
	assert.Equal(t, 1, projects[0].ID)
	assert.Equal(t, "Mobile App", projects[0].Name)
	assert.Equal(t, 2, projects[1].ID)
	assert.Equal(t, "Web App", projects[1].Name)
}

func TestListProjects_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"projects": []}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	projects, err := client.ListProjects(context.Background(), "team123")
	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestListProjectFiles_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/42/files", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"files": [
				{
					"key": "file1",
					"name": "Homepage",
					"thumbnail_url": "https://figma.com/thumb/1",
					"last_modified": "2025-01-15T10:30:00Z"
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	files, err := client.ListProjectFiles(context.Background(), "42")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "file1", files[0].Key)
	assert.Equal(t, "Homepage", files[0].Name)
}

func TestListProjectFiles_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"files": []}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "figd_test")
	files, err := client.ListProjectFiles(context.Background(), "42")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestEncodeNodeIDs(t *testing.T) {
	// Node IDs with colons should be URL-encoded
	encoded := encodeNodeIDs([]string{"0:1", "1:234"})
	assert.Contains(t, encoded, "0%3A1")
	assert.Contains(t, encoded, "1%3A234")
}
