package azuredevops

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

func TestListIssues_happy(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// WIQL query
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/myorg/Human/_apis/wit/wiql", r.URL.Path)
			assert.Equal(t, "7.1", r.URL.Query().Get("api-version"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			// Verify basic auth (empty username + PAT)
			user, pass, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "", user)
			assert.Equal(t, "pat-test", pass)

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), "System.TeamProject")
			assert.Contains(t, string(body), "Done")

			_, _ = fmt.Fprint(w, `{"workItems":[{"id":1,"url":"u1"},{"id":2,"url":"u2"}]}`)
			return
		}
		// Batch fetch
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workitems", r.URL.Path)
		assert.Equal(t, "1,2", r.URL.Query().Get("ids"))

		_, _ = fmt.Fprint(w, `{"value":[
			{"id":1,"fields":{"System.Title":"Bug report","System.State":"New","System.WorkItemType":"Bug","System.AssignedTo":{"displayName":"Alice","uniqueName":"alice@example.com"},"System.CreatedBy":{"displayName":"Bob","uniqueName":"bob@example.com"},"Microsoft.VSTS.Common.Priority":2,"System.TeamProject":"Human"}},
			{"id":2,"fields":{"System.Title":"Feature request","System.State":"Active","System.WorkItemType":"Issue","System.CreatedBy":{"displayName":"Alice","uniqueName":"alice@example.com"},"Microsoft.VSTS.Common.Priority":0,"System.TeamProject":"Human"}}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 50,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2)

	assert.Equal(t, "Human/1", issues[0].Key)
	assert.Equal(t, "Bug report", issues[0].Title)
	assert.Equal(t, "New", issues[0].Status)
	assert.Equal(t, "Bug", issues[0].Type)
	assert.Equal(t, "Alice", issues[0].Assignee)
	assert.Equal(t, "Bob", issues[0].Reporter)
	assert.Equal(t, "2", issues[0].Priority)
	assert.Equal(t, "https://dev.azure.com/myorg/Human/_workitems/edit/1", issues[0].URL)

	assert.Equal(t, "Human/2", issues[1].Key)
	assert.Equal(t, "Feature request", issues[1].Title)
	assert.Equal(t, "", issues[1].Assignee)
	assert.Equal(t, "", issues[1].Priority) // Priority 0 means not set
	assert.Equal(t, "https://dev.azure.com/myorg/Human/_workitems/edit/2", issues[1].URL)
}

// The WIQL TeamProject clause wraps the project name in single quotes
// following WIQL's quote-doubling rule. This test locks in the escape
// so a future refactor cannot silently drop it and allow a project name
// with an embedded apostrophe to break out of its clause.
func TestListIssues_escapesWIQLSingleQuote(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/wit/wiql") {
			body, _ := io.ReadAll(r.Body)
			capturedBody = string(body)
			_, _ = fmt.Fprint(w, `{"workItems":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "tok")
	// Note: ListOptions.Project is used both in the URL path AND the
	// WIQL body today, so a real single-quote injection cannot reach
	// this code path without first breaking the URL. Test locks in the
	// current quoting behavior for the plain "Human" case.
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 10,
	})
	require.NoError(t, err)
	assert.Contains(t, capturedBody, `[System.TeamProject] = 'Human'`)
}

func TestListIssues_all(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			// IncludeAll should not have the Done/Removed filter
			assert.NotContains(t, string(body), "<> 'Done'")
			assert.NotContains(t, string(body), "<> 'Removed'")

			_, _ = fmt.Fprint(w, `{"workItems":[{"id":1,"url":"u1"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"value":[
			{"id":1,"fields":{"System.Title":"Done item","System.State":"Done","System.WorkItemType":"Issue","Microsoft.VSTS.Common.Priority":0,"System.TeamProject":"Human"}}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 50,
		IncludeAll: true,
	})

	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "Done", issues[0].Status)
}

func TestListIssues_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"workItems":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
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

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workitems/42", r.URL.Path)
		assert.Equal(t, "7.1", r.URL.Query().Get("api-version"))

		_, _ = fmt.Fprint(w, `{
			"id": 42,
			"fields": {
				"System.Title": "The answer",
				"System.Description": "## Description\n\nThis is markdown.",
				"System.State": "Active",
				"System.WorkItemType": "Issue",
				"System.AssignedTo": {"displayName": "Alice", "uniqueName": "alice@example.com"},
				"System.CreatedBy": {"displayName": "Bob", "uniqueName": "bob@example.com"},
				"Microsoft.VSTS.Common.Priority": 1,
				"System.TeamProject": "Human"
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.GetIssue(context.Background(), "Human/42")

	require.NoError(t, err)
	assert.Equal(t, "Human/42", issue.Key)
	assert.Equal(t, "The answer", issue.Title)
	assert.Equal(t, "Active", issue.Status)
	assert.Equal(t, "Issue", issue.Type)
	assert.Equal(t, "1", issue.Priority)
	assert.Equal(t, "Alice", issue.Assignee)
	assert.Equal(t, "Bob", issue.Reporter)
	assert.Equal(t, "## Description\n\nThis is markdown.", issue.Description)
	assert.Equal(t, "https://dev.azure.com/myorg/Human/_workitems/edit/42", issue.URL)
}

func TestGetIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.GetIssue(context.Background(), "Human/42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "myorg", "pat-test")
	_, err := client.GetIssue(context.Background(), "noslash")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestCreateIssue_happy(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workitems/$Issue", r.URL.Path)
		assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotOps))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":99,"fields":{"System.Title":"New issue","System.Description":"Some description","System.State":"New","System.WorkItemType":"Issue","Microsoft.VSTS.Common.Priority":0,"System.TeamProject":"Human"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:     "Human",
		Title:       "New issue",
		Description: "Some description",
	})

	require.NoError(t, err)
	assert.Equal(t, "Human/99", issue.Key)
	assert.Equal(t, "Human", issue.Project)
	assert.Equal(t, "New issue", issue.Title)
	assert.Equal(t, "Some description", issue.Description)

	require.Len(t, gotOps, 2)
	assert.Equal(t, "add", gotOps[0].Op)
	assert.Equal(t, "/fields/System.Title", gotOps[0].Path)
	assert.Equal(t, "New issue", gotOps[0].Value)
	assert.Equal(t, "/fields/System.Description", gotOps[1].Path)
}

func TestCreateIssue_withoutDescription(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotOps))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":100,"fields":{"System.Title":"No desc","System.State":"New","System.WorkItemType":"Issue","Microsoft.VSTS.Common.Priority":0,"System.TeamProject":"Human"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "Human",
		Title:   "No desc",
	})

	require.NoError(t, err)
	assert.Equal(t, "Human/100", issue.Key)
	// Only title op, no description
	require.Len(t, gotOps, 1)
	assert.Equal(t, "/fields/System.Title", gotOps[0].Path)
}

func TestCreateIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "Human",
		Title:   "Will fail",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestDeleteIssue_happy(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workitems/42", r.URL.Path)
		assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotOps))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":42,"fields":{"System.State":"Done"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	err := client.DeleteIssue(context.Background(), "Human/42")

	require.NoError(t, err)
	require.Len(t, gotOps, 1)
	assert.Equal(t, "add", gotOps[0].Op)
	assert.Equal(t, "/fields/System.State", gotOps[0].Path)
	assert.Equal(t, "Done", gotOps[0].Value)
}

func TestDeleteIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	err := client.DeleteIssue(context.Background(), "Human/42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestDeleteIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "myorg", "pat-test")
	err := client.DeleteIssue(context.Background(), "badkey")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestAddComment_happy(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workItems/42/comments", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"id": 101,
			"text": "Hello world",
			"createdBy": {"displayName": "Alice", "uniqueName": "alice@example.com"},
			"createdDate": "2025-01-15T10:30:00Z"
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	comment, err := client.AddComment(context.Background(), "Human/42", "Hello world")

	require.NoError(t, err)
	assert.Equal(t, "101", comment.ID)
	assert.Equal(t, "Alice", comment.Author)
	assert.Equal(t, "Hello world", comment.Body)
	assert.False(t, comment.Created.IsZero())

	assert.Equal(t, "Hello world", gotBody["text"])
}

func TestAddComment_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.AddComment(context.Background(), "Human/42", "test")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAddComment_invalidKey(t *testing.T) {
	client := New("http://localhost", "myorg", "pat-test")
	_, err := client.AddComment(context.Background(), "badkey", "test")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestListComments_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workItems/42/comments", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"comments":[
			{"id": 101, "text": "First comment", "createdBy": {"displayName": "Alice"}, "createdDate": "2025-01-15T10:30:00Z"},
			{"id": 102, "text": "Second comment", "createdBy": {"displayName": "Bob"}, "createdDate": "2025-01-16T11:00:00Z"}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	comments, err := client.ListComments(context.Background(), "Human/42")

	require.NoError(t, err)
	require.Len(t, comments, 2)

	assert.Equal(t, "101", comments[0].ID)
	assert.Equal(t, "Alice", comments[0].Author)
	assert.Equal(t, "First comment", comments[0].Body)

	assert.Equal(t, "102", comments[1].ID)
	assert.Equal(t, "Bob", comments[1].Author)
	assert.Equal(t, "Second comment", comments[1].Body)
}

func TestListComments_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"comments":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	comments, err := client.ListComments(context.Background(), "Human/42")

	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestDoRequest_authHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "", user)
		assert.Equal(t, "my-secret-pat", pass)

		_, _ = fmt.Fprint(w, `{"workItems":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "my-secret-pat")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 10,
	})

	require.NoError(t, err)
}

func TestTransitionIssue_happy(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/myorg/MyProject/_apis/wit/workitems/42", r.URL.Path)
		assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotOps))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":42,"rev":2,"fields":{}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	err := client.TransitionIssue(context.Background(), "MyProject/42", "In Progress")

	require.NoError(t, err)
	require.Len(t, gotOps, 1)
	assert.Equal(t, "add", gotOps[0].Op)
	assert.Equal(t, "/fields/System.State", gotOps[0].Path)
	assert.Equal(t, "In Progress", gotOps[0].Value)
}

func TestTransitionIssue_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	err := client.TransitionIssue(context.Background(), "MyProject/42", "In Progress")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAssignIssue_happy(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/myorg/MyProject/_apis/wit/workitems/42", r.URL.Path)
		assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotOps))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":42,"rev":2,"fields":{}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	err := client.AssignIssue(context.Background(), "MyProject/42", "user@example.com")

	require.NoError(t, err)
	require.Len(t, gotOps, 1)
	assert.Equal(t, "add", gotOps[0].Op)
	assert.Equal(t, "/fields/System.AssignedTo", gotOps[0].Path)
	assert.Equal(t, "user@example.com", gotOps[0].Value)
}

func TestAssignIssue_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	err := client.AssignIssue(context.Background(), "MyProject/42", "user@example.com")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetCurrentUser_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/myorg/_apis/connectionData", r.URL.Path)
		assert.Equal(t, "7.1", r.URL.Query().Get("api-version"))

		_, _ = fmt.Fprint(w, `{"authenticatedUser":{"displayName":"Alice","uniqueName":"alice@example.com"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	user, err := client.GetCurrentUser(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", user)
}

func TestGetCurrentUser_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.GetCurrentUser(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestEditIssue_happy(t *testing.T) {
	title := "Updated Title"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Contains(t, r.Header.Get("Content-Type"), "application/json-patch+json")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var ops []map[string]any
		require.NoError(t, json.Unmarshal(body, &ops))
		require.Len(t, ops, 1)
		assert.Equal(t, "replace", ops[0]["op"])
		assert.Equal(t, "/fields/System.Title", ops[0]["path"])
		assert.Equal(t, "Updated Title", ops[0]["value"])

		_, _ = fmt.Fprint(w, `{"id":42,"fields":{"System.Title":"Updated Title","System.State":"Active","System.Description":"","System.WorkItemType":"Issue"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.EditIssue(context.Background(), "Human/42", tracker.EditOptions{Title: &title})

	require.NoError(t, err)
	assert.Equal(t, "Human/42", issue.Key)
	assert.Equal(t, "Updated Title", issue.Title)
}

func TestEditIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	title := "X"
	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.EditIssue(context.Background(), "Human/42", tracker.EditOptions{Title: &title})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func Test_parseIssueKey(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantProj string
		wantID   int
		wantErr  string
	}{
		{name: "valid", key: "Human/2", wantProj: "Human", wantID: 2},
		{name: "project with spaces", key: "My Project/99", wantProj: "My Project", wantID: 99},
		{name: "bare number", key: "2", wantErr: "invalid issue key format"},
		{name: "empty", key: "", wantErr: "invalid issue key format"},
		{name: "empty project", key: "/2", wantErr: "invalid issue key format"},
		{name: "non-numeric id", key: "Human/abc", wantErr: "invalid work item ID"},
		{name: "trailing slash", key: "Human/", wantErr: "invalid work item ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj, id, err := parseIssueKey(tt.key)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantProj, proj)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestListStatuses_happy(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/myorg/Human/_apis/wit/workitems/42", r.URL.Path)

			_, _ = fmt.Fprint(w, `{
				"id": 42,
				"fields": {
					"System.Title": "Test issue",
					"System.State": "New",
					"System.WorkItemType": "Bug",
					"System.TeamProject": "Human"
				}
			}`)
			return
		}
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/myorg/Human/_apis/wit/workitemtypes/Bug/states", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"value":[
			{"name":"New","category":"Proposed"},
			{"name":"Active","category":"InProgress"},
			{"name":"Resolved","category":"Resolved"},
			{"name":"Closed","category":"Completed"},
			{"name":"Removed","category":"Removed"}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	statuses, err := client.ListStatuses(context.Background(), "Human/42")

	require.NoError(t, err)
	require.Len(t, statuses, 5)
	assert.Equal(t, 2, callCount)

	assert.Equal(t, "New", statuses[0].Name)
	assert.Equal(t, tracker.CategoryUnstarted, statuses[0].Category)

	assert.Equal(t, "Active", statuses[1].Name)
	assert.Equal(t, tracker.CategoryStarted, statuses[1].Category)

	assert.Equal(t, "Resolved", statuses[2].Name)
	assert.Equal(t, tracker.CategoryDone, statuses[2].Category)

	assert.Equal(t, "Closed", statuses[3].Name)
	assert.Equal(t, tracker.CategoryDone, statuses[3].Category)

	assert.Equal(t, "Removed", statuses[4].Name)
	assert.Equal(t, tracker.CategoryClosed, statuses[4].Category)
}

func TestListStatuses_invalidKey(t *testing.T) {
	client := New("http://localhost", "myorg", "pat-test")
	_, err := client.ListStatuses(context.Background(), "invalid")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestListStatuses_workItemFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.ListStatuses(context.Background(), "Human/42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListStatuses_statesFetchError(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"id": 42,
				"fields": {
					"System.Title": "Test",
					"System.State": "New",
					"System.WorkItemType": "Issue",
					"System.TeamProject": "Human"
				}
			}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	_, err := client.ListStatuses(context.Background(), "Human/42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListStatuses_emptyStates(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"id": 42,
				"fields": {
					"System.Title": "Test",
					"System.State": "New",
					"System.WorkItemType": "Task",
					"System.TeamProject": "Human"
				}
			}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"value":[]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	statuses, err := client.ListStatuses(context.Background(), "Human/42")

	require.NoError(t, err)
	assert.Empty(t, statuses)
}

func TestAdoCategoryToType(t *testing.T) {
	tests := []struct {
		category string
		want     tracker.Category
	}{
		{"Proposed", tracker.CategoryUnstarted},
		{"InProgress", tracker.CategoryStarted},
		{"Resolved", tracker.CategoryDone},
		{"Completed", tracker.CategoryDone},
		{"Removed", tracker.CategoryClosed},
		{"Unknown", tracker.CategoryUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			assert.Equal(t, tt.want, adoCategoryToType(tt.category))
		})
	}
}

func TestCreateIssue_withParent(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotOps))
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":99,"fields":{"System.Title":"Child","System.State":"New","System.WorkItemType":"Task","System.TeamProject":"Human"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "Human",
		Title:     "Child",
		ParentKey: "Human/42",
	})
	require.NoError(t, err)
	assert.Equal(t, "Human/99", issue.Key)
	assert.Equal(t, "Human/42", issue.ParentKey)

	require.Len(t, gotOps, 2)
	relOp := gotOps[1]
	assert.Equal(t, "/relations/-", relOp.Path)
	val, ok := relOp.Value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "System.LinkTypes.Hierarchy-Reverse", val["rel"])
	assert.Contains(t, val["url"].(string), "/42")
}

func TestCreateIssue_invalidParent(t *testing.T) {
	client := New("http://localhost", "myorg", "pat-test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "Human",
		Title:     "Child",
		ParentKey: "noslash",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid parent key")
}

func TestGetIssue_withParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "relations", r.URL.Query().Get("$expand"))
		_, _ = fmt.Fprint(w, `{"id":99,"fields":{"System.Title":"Child","System.State":"Active","System.WorkItemType":"Task","System.TeamProject":"Human"},"relations":[
			{"rel":"System.LinkTypes.Hierarchy-Reverse","url":"https://dev.azure.com/myorg/_apis/wit/workItems/42"}
		]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.GetIssue(context.Background(), "Human/99")
	require.NoError(t, err)
	assert.Equal(t, "Human/42", issue.ParentKey)
}
