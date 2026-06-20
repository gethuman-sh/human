package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/tracker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_hasDescription(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{"nil raw message", nil, false},
		{"empty raw message", json.RawMessage{}, false},
		{"null string", json.RawMessage(`null`), false},
		{"valid JSON object", json.RawMessage(`{"type":"doc"}`), true},
		{"empty JSON object", json.RawMessage(`{}`), true},
		{"string value", json.RawMessage(`"hello"`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasDescription(tt.raw))
		})
	}
}

func Test_nameOrEmpty(t *testing.T) {
	tests := []struct {
		name  string
		field *nameField
		want  string
	}{
		{"nil returns empty", nil, ""},
		{"display name preferred", &nameField{DisplayName: "Alice", Name: "alice"}, "Alice"},
		{"falls back to name", &nameField{Name: "bob"}, "bob"},
		{"both empty returns empty", &nameField{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, nameOrEmpty(tt.field))
		})
	}
}

func TestCreateIssue_happy(t *testing.T) {
	var gotBody createRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/rest/api/3/issue", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"10001","key":"KAN-42"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:     "KAN",
		Type:        "Task",
		Title:       "Test issue",
		Description: "Some description",
	})

	require.NoError(t, err)
	assert.Equal(t, "KAN-42", issue.Key)
	assert.Equal(t, "KAN", issue.Project)
	assert.Equal(t, "Task", issue.Type)
	assert.Equal(t, "Test issue", issue.Title)
	assert.Equal(t, "Some description", issue.Description)
	assert.Equal(t, srv.URL+"/browse/KAN-42", issue.URL)

	assert.Equal(t, "KAN", gotBody.Fields.Project.Key)
	assert.Equal(t, "Task", gotBody.Fields.IssueType.Name)
	assert.Equal(t, "Test issue", gotBody.Fields.Summary)
	assert.NotNil(t, gotBody.Fields.Description)
}

func TestCreateIssue_withoutDescription(t *testing.T) {
	var gotBody createRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"10002","key":"KAN-43"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "KAN",
		Type:    "Bug",
		Title:   "No description issue",
	})

	require.NoError(t, err)
	assert.Equal(t, "KAN-43", issue.Key)
	assert.Nil(t, gotBody.Fields.Description)
}

func TestCreateIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "KAN",
		Type:    "Task",
		Title:   "Will fail",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListIssues_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/rest/api/3/search/jql", r.URL.Path)
		jql := r.URL.Query().Get("jql")
		assert.Contains(t, jql, `project="KAN"`)
		assert.Contains(t, jql, "statusCategory != Done")
		assert.Equal(t, "10", r.URL.Query().Get("maxResults"))

		_, _ = fmt.Fprint(w, `{"issues":[
			{"key":"KAN-1","fields":{"summary":"First issue","status":{"name":"To Do"},"issuetype":{"name":"Bug"}}},
			{"key":"KAN-2","fields":{"summary":"Second issue","status":{"name":"In Progress"},"issuetype":{"name":"Task"}}}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "KAN",
		MaxResults: 10,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2)

	assert.Equal(t, "KAN-1", issues[0].Key)
	assert.Equal(t, "First issue", issues[0].Title)
	assert.Equal(t, "To Do", issues[0].Status)
	assert.Equal(t, "Bug", issues[0].Type)
	assert.Equal(t, srv.URL+"/browse/KAN-1", issues[0].URL)

	assert.Equal(t, "KAN-2", issues[1].Key)
	assert.Equal(t, "Second issue", issues[1].Title)
	assert.Equal(t, "In Progress", issues[1].Status)
	assert.Equal(t, "Task", issues[1].Type)
	assert.Equal(t, srv.URL+"/browse/KAN-2", issues[1].URL)
}

// A project name carrying a double-quote must be escaped so the JQL stays
// scoped to a single project clause. Without the escape, an attacker
// could inject `KAN" OR project="OTHER` to widen the query across
// projects they should not see.
func TestListIssues_escapesJQLQuotes(t *testing.T) {
	var capturedJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedJQL = r.URL.Query().Get("jql")
		_, _ = fmt.Fprint(w, `{"issues":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "u", "k")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    `KAN" OR project="OTHER`,
		MaxResults: 10,
	})
	require.NoError(t, err)

	// The attacker substring must land inside the quoted project clause
	// with both inner quotes escaped via backslash.
	assert.Contains(t, capturedJQL, `project="KAN\" OR project=\"OTHER"`)
	assert.True(t, strings.HasPrefix(capturedJQL, `project=`),
		"JQL must still start with the project clause, got %q", capturedJQL)
}

func TestListIssues_all(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jql := r.URL.Query().Get("jql")
		assert.Contains(t, jql, `project="KAN"`)
		assert.NotContains(t, jql, "statusCategory")

		_, _ = fmt.Fprint(w, `{"issues":[
			{"key":"KAN-1","fields":{"summary":"Open issue","status":{"name":"To Do"}}},
			{"key":"KAN-2","fields":{"summary":"Done issue","status":{"name":"Done"}}}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "KAN",
		MaxResults: 10,
		IncludeAll: true,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, "To Do", issues[0].Status)
	assert.Equal(t, "Done", issues[1].Status)
}

func TestListIssues_emptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"issues":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "KAN",
		MaxResults: 10,
	})

	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestListIssues_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "KAN",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/rest/api/3/issue/KAN-42", r.URL.Path)

		assert.Contains(t, r.URL.RawQuery, "issuetype", "GetIssue must request issuetype so bug detection works")
		_, _ = fmt.Fprint(w, `{
			"key": "KAN-42",
			"fields": {
				"summary": "The answer",
				"status": {"name": "Done"},
				"priority": {"displayName": "High", "name": "High"},
				"assignee": {"displayName": "Alice", "name": "alice"},
				"reporter": {"displayName": "Bob", "name": "bob"},
				"description": {"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]},
				"issuetype": {"name": "Bug"}
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.GetIssue(context.Background(), "KAN-42")

	require.NoError(t, err)
	assert.Equal(t, "KAN-42", issue.Key)
	assert.Equal(t, "The answer", issue.Title)
	assert.Equal(t, "Done", issue.Status)
	assert.Equal(t, "Bug", issue.Type)
	assert.Equal(t, "High", issue.Priority)
	assert.Equal(t, "Alice", issue.Assignee)
	assert.Equal(t, "Bob", issue.Reporter)
	assert.Contains(t, issue.Description, "Hello world")
}

func TestGetIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	_, err := client.GetIssue(context.Background(), "KAN-42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAddComment_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/rest/api/3/issue/KAN-1/comment", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var got commentBody
		require.NoError(t, json.Unmarshal(body, &got))
		assert.NotNil(t, got.Body, "body should be ADF document")

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{
			"id": "10042",
			"author": {"displayName": "Alice"},
			"body": {"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]},
			"created": "2025-01-15T10:30:00.000+0000"
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	comment, err := client.AddComment(context.Background(), "KAN-1", "Hello world")

	require.NoError(t, err)
	assert.Equal(t, "10042", comment.ID)
	assert.Equal(t, "Alice", comment.Author)
	assert.Contains(t, comment.Body, "Hello world")
	assert.False(t, comment.Created.IsZero())
}

func TestAddComment_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	_, err := client.AddComment(context.Background(), "KAN-1", "test")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListComments_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/rest/api/3/issue/KAN-1/comment", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"comments":[
			{
				"id": "10001",
				"author": {"displayName": "Alice"},
				"body": {"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"First comment"}]}]},
				"created": "2025-01-15T10:30:00.000+0000"
			},
			{
				"id": "10002",
				"author": {"displayName": "Bob"},
				"body": {"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Second comment"}]}]},
				"created": "2025-01-16T11:00:00.000+0000"
			}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	comments, err := client.ListComments(context.Background(), "KAN-1")

	require.NoError(t, err)
	require.Len(t, comments, 2)

	assert.Equal(t, "10001", comments[0].ID)
	assert.Equal(t, "Alice", comments[0].Author)
	assert.Contains(t, comments[0].Body, "First comment")

	assert.Equal(t, "10002", comments[1].ID)
	assert.Equal(t, "Bob", comments[1].Author)
	assert.Contains(t, comments[1].Body, "Second comment")
}

func TestDoRequest_authHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok, "expected Basic auth to be set")
		assert.Equal(t, "user@example.com", user)
		assert.Equal(t, "api-token-123", pass)

		_, _ = fmt.Fprint(w, `{"issues":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "api-token-123")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "KAN",
		MaxResults: 10,
	})

	require.NoError(t, err)
}

func TestDeleteIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/rest/api/3/issue/KAN-42", r.URL.Path)

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	err := client.DeleteIssue(context.Background(), "KAN-42")

	require.NoError(t, err)
}

func TestDeleteIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	err := client.DeleteIssue(context.Background(), "KAN-42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListComments_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"comments":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	comments, err := client.ListComments(context.Background(), "KAN-1")

	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestTransitionIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/api/3/issue/KAN-1/transitions", r.URL.Path)

		switch r.Method {
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"transitions":[{"id":"21","name":"Start Progress","to":{"name":"In Progress"}}]}`)
		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var got map[string]map[string]string
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, "21", got["transition"]["id"])

			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	err := client.TransitionIssue(context.Background(), "KAN-1", "In Progress")

	require.NoError(t, err)
}

func TestTransitionIssue_notFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"transitions":[{"id":"31","name":"Done","to":{"name":"Done"}}]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	err := client.TransitionIssue(context.Background(), "KAN-1", "In Progress")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "transition not found")
}

func TestAssignIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/rest/api/3/issue/KAN-1/assignee", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var got map[string]string
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "user-123", got["accountId"])

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	err := client.AssignIssue(context.Background(), "KAN-1", "user-123")

	require.NoError(t, err)
}

func TestAssignIssue_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	err := client.AssignIssue(context.Background(), "KAN-1", "user-123")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetCurrentUser_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/rest/api/3/myself", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"accountId":"abc-123"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	accountID, err := client.GetCurrentUser(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "abc-123", accountID)
}

func TestGetCurrentUser_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	_, err := client.GetCurrentUser(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestEditIssue_happy(t *testing.T) {
	callCount := 0
	title := "Updated Title"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/3/issue/KAN-1":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var got map[string]map[string]any
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, "Updated Title", got["fields"]["summary"])
			assert.NotNil(t, got["fields"]["description"])

			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/KAN-1":
			_, _ = fmt.Fprint(w, `{
				"key": "KAN-1",
				"fields": {
					"summary": "Updated Title",
					"status": {"name": "Open"},
					"description": null
				}
			}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	desc := "New desc"
	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{
		Title:       &title,
		Description: &desc,
	})

	require.NoError(t, err)
	assert.Equal(t, "KAN-1", issue.Key)
	assert.Equal(t, "Updated Title", issue.Title)
	assert.Equal(t, 2, callCount)
}

func TestEditIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	title := "X"
	client := New(srv.URL, "user@example.com", "token")
	_, err := client.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{Title: &title})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListStatuses_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/rest/api/3/issue/KAN-42/transitions", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"transitions":[
			{"id":"21","name":"Start Progress","to":{"name":"In Progress"}},
			{"id":"22","name":"Done","to":{"name":"Done"}},
			{"id":"23","name":"Backlog","to":{"name":"To Do"}}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	statuses, err := client.ListStatuses(context.Background(), "KAN-42")

	require.NoError(t, err)
	require.Len(t, statuses, 3)

	assert.Equal(t, "In Progress", statuses[0].Name)
	assert.Equal(t, "Done", statuses[1].Name)
	assert.Equal(t, "To Do", statuses[2].Name)
}

func TestListStatuses_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"transitions":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	statuses, err := client.ListStatuses(context.Background(), "KAN-42")

	require.NoError(t, err)
	assert.Empty(t, statuses)
}

func TestListStatuses_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	_, err := client.ListStatuses(context.Background(), "KAN-42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestCreateIssue_withParent(t *testing.T) {
	var gotBody createRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"10005","key":"KAN-50"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "KAN",
		Type:      "Sub-task",
		Title:     "Child",
		ParentKey: "KAN-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "KAN-50", issue.Key)
	assert.Equal(t, "KAN-1", issue.ParentKey)
	require.NotNil(t, gotBody.Fields.Parent)
	assert.Equal(t, "KAN-1", gotBody.Fields.Parent.Key)
}

func TestGetIssue_withParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Query().Get("fields"), "parent")
		_, _ = fmt.Fprint(w, `{"key":"KAN-50","fields":{"summary":"Child","status":{"name":"To Do"},"issuetype":{"name":"Sub-task"},"parent":{"key":"KAN-1"}}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "user@example.com", "token")
	issue, err := client.GetIssue(context.Background(), "KAN-50")
	require.NoError(t, err)
	assert.Equal(t, "KAN-1", issue.ParentKey)
}
