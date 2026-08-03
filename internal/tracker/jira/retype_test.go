package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// Jira names the kind natively, so a retype is the issuetype field on the same
// edit call create already uses (SC-3051).
func TestEditIssue_retypeWritesIssueType(t *testing.T) {
	var gotFields map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/3/issue/KAN-1":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var got map[string]map[string]any
			require.NoError(t, json.Unmarshal(body, &got))
			gotFields = got["fields"]
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/KAN-1":
			_, _ = fmt.Fprint(w, `{"key":"KAN-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"}}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	bug := "Bug"
	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{Type: &bug})

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"name": "Bug"}, gotFields["issuetype"])
	assert.NotContains(t, gotFields, "summary", "a retype must not touch the title")
	assert.Equal(t, "Bug", issue.Type)
}

// The reverse direction is the same one field — a defect becoming ordinary
// product work must be as ordinary an edit as the other way round.
func TestEditIssue_retypeBackToProductWork(t *testing.T) {
	var gotFields map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var got map[string]map[string]any
			require.NoError(t, json.Unmarshal(body, &got))
			gotFields = got["fields"]
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = fmt.Fprint(w, `{"key":"KAN-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Task"}}}`)
		}
	}))
	defer srv.Close()

	task := "Task"
	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{Type: &task})

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"name": "Task"}, gotFields["issuetype"])
	assert.Equal(t, "Task", issue.Type)
}
