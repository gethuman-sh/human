package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/tracker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListIssues_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues", r.URL.Path)
		assert.Equal(t, "50", r.URL.Query().Get("per_page"))
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")

		// Return 2 issues + 1 PR (should be filtered).
		_, _ = fmt.Fprint(w, `[
			{"number":1,"html_url":"https://github.com/octocat/hello-world/issues/1","title":"Bug report","body":"desc1","state":"open","user":{"login":"alice"},"assignee":{"login":"bob"},"labels":[{"name":"bug"}]},
			{"number":2,"html_url":"https://github.com/octocat/hello-world/issues/2","title":"Feature request","body":"desc2","state":"open","user":{"login":"alice"},"labels":[]},
			{"number":3,"html_url":"https://github.com/octocat/hello-world/pull/3","title":"A pull request","body":"pr body","state":"open","user":{"login":"charlie"},"pull_request":{}}
		]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "octocat/hello-world",
		MaxResults: 50,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2)

	assert.Equal(t, "octocat/hello-world#1", issues[0].Key)
	assert.Equal(t, "Bug report", issues[0].Title)
	assert.Equal(t, "open", issues[0].Status)
	assert.Equal(t, "bug", issues[0].Type)
	assert.Equal(t, "bob", issues[0].Assignee)
	assert.Equal(t, "alice", issues[0].Reporter)
	assert.Equal(t, "https://github.com/octocat/hello-world/issues/1", issues[0].URL)

	assert.Equal(t, "octocat/hello-world#2", issues[1].Key)
	assert.Equal(t, "", issues[1].Type) // no labels
	assert.Equal(t, "", issues[1].Assignee)
	assert.Equal(t, "https://github.com/octocat/hello-world/issues/2", issues[1].URL)
}

func TestListIssues_all(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "all", r.URL.Query().Get("state"))

		_, _ = fmt.Fprint(w, `[
			{"number":1,"title":"Open issue","body":"","state":"open","user":{"login":"alice"},"labels":[]},
			{"number":2,"title":"Closed issue","body":"","state":"closed","user":{"login":"alice"},"labels":[]}
		]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "octocat/hello-world",
		MaxResults: 50,
		IncludeAll: true,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, "open", issues[0].Status)
	assert.Equal(t, "closed", issues[1].Status)
}

func TestListIssues_emptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "octocat/hello-world",
		MaxResults: 10,
	})

	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestListIssues_invalidProject(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "noslash",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid project format")
}

func TestListIssues_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "octocat/hello-world",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues/42", r.URL.Path)

		_, _ = fmt.Fprint(w, `{
			"number": 42,
			"title": "The answer",
			"body": "## Description\n\nThis is markdown.",
			"state": "open",
			"user": {"login": "alice"},
			"assignee": {"login": "bob"},
			"labels": [{"name": "enhancement"}, {"name": "help wanted"}]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issue, err := client.GetIssue(context.Background(), "octocat/hello-world#42")

	require.NoError(t, err)
	assert.Equal(t, "octocat/hello-world#42", issue.Key)
	assert.Equal(t, "The answer", issue.Title)
	assert.Equal(t, "open", issue.Status)
	assert.Equal(t, "enhancement", issue.Type)                            // first label
	assert.Equal(t, []string{"enhancement", "help wanted"}, issue.Labels) // full label set, for IsBug() and other consumers
	assert.Equal(t, "", issue.Priority)                                   // GitHub has no priority
	assert.Equal(t, "bob", issue.Assignee)
	assert.Equal(t, "alice", issue.Reporter)
	assert.Equal(t, "## Description\n\nThis is markdown.", issue.Description)
}

func TestGetIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.GetIssue(context.Background(), "octocat/hello-world#42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	_, err := client.GetIssue(context.Background(), "nohash")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestGetIssue_invalidNumber(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	_, err := client.GetIssue(context.Background(), "octocat/hello-world#abc")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue number")
}

func TestCreateIssue_happy(t *testing.T) {
	var gotBody createRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"number":99,"title":"New issue","body":"Some description"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:     "octocat/hello-world",
		Title:       "New issue",
		Description: "Some description",
	})

	require.NoError(t, err)
	assert.Equal(t, "octocat/hello-world#99", issue.Key)
	assert.Equal(t, "octocat/hello-world", issue.Project)
	assert.Equal(t, "New issue", issue.Title)
	assert.Equal(t, "Some description", issue.Description)

	assert.Equal(t, "New issue", gotBody.Title)
	assert.Equal(t, "Some description", gotBody.Body)
}

func TestCreateIssue_withoutDescription(t *testing.T) {
	var gotRaw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotRaw))

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"number":100,"title":"No desc issue","body":""}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "octocat/hello-world",
		Title:   "No desc issue",
	})

	require.NoError(t, err)
	assert.Equal(t, "octocat/hello-world#100", issue.Key)
	// body should be omitted from JSON when empty
	_, hasBody := gotRaw["body"]
	assert.False(t, hasBody, "body should be omitted when empty")
}

func TestCreateIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "octocat/hello-world",
		Title:   "Will fail",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func Test_splitProject(t *testing.T) {
	tests := []struct {
		name      string
		project   string
		wantOwner string
		wantRepo  string
		wantErr   string
	}{
		{name: "valid", project: "octocat/hello-world", wantOwner: "octocat", wantRepo: "hello-world"},
		{name: "with dots", project: "org/repo.name", wantOwner: "org", wantRepo: "repo.name"},
		{name: "no slash", project: "noslash", wantErr: "invalid project format"},
		{name: "empty owner", project: "/repo", wantErr: "invalid project format"},
		{name: "empty repo", project: "owner/", wantErr: "invalid project format"},
		{name: "empty string", project: "", wantErr: "invalid project format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := splitProject(tt.project)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
		})
	}
}

func Test_parseIssueKey(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantErr    string
	}{
		{name: "valid", key: "octocat/hello-world#42", wantOwner: "octocat", wantRepo: "hello-world", wantNumber: 42},
		{name: "large number", key: "org/repo#99999", wantOwner: "org", wantRepo: "repo", wantNumber: 99999},
		{name: "no hash", key: "octocat/hello-world", wantErr: "invalid issue key format"},
		{name: "no slash", key: "noslash#42", wantErr: "invalid issue key format"},
		{name: "non-numeric", key: "octocat/hello-world#abc", wantErr: "invalid issue number"},
		{name: "empty", key: "", wantErr: "invalid issue key format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := parseIssueKey(tt.key)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantNumber, number)
		})
	}
}

func TestAddComment_happy(t *testing.T) {
	var gotBody commentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues/42/comments", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{
			"id": 101,
			"body": "Hello world",
			"user": {"login": "alice"},
			"created_at": "2025-01-15T10:30:00Z"
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	comment, err := client.AddComment(context.Background(), "octocat/hello-world#42", "Hello world")

	require.NoError(t, err)
	assert.Equal(t, "101", comment.ID)
	assert.Equal(t, "alice", comment.Author)
	assert.Equal(t, "Hello world", comment.Body)
	assert.False(t, comment.Created.IsZero())

	assert.Equal(t, "Hello world", gotBody.Body)
}

func TestAddComment_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.AddComment(context.Background(), "octocat/hello-world#42", "test")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAddComment_invalidKey(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	_, err := client.AddComment(context.Background(), "badkey", "test")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestListComments_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues/42/comments", r.URL.Path)

		_, _ = fmt.Fprint(w, `[
			{"id": 101, "body": "First comment", "user": {"login": "alice"}, "created_at": "2025-01-15T10:30:00Z"},
			{"id": 102, "body": "Second comment", "user": {"login": "bob"}, "created_at": "2025-01-16T11:00:00Z"}
		]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	comments, err := client.ListComments(context.Background(), "octocat/hello-world#42")

	require.NoError(t, err)
	require.Len(t, comments, 2)

	assert.Equal(t, "101", comments[0].ID)
	assert.Equal(t, "alice", comments[0].Author)
	assert.Equal(t, "First comment", comments[0].Body)

	assert.Equal(t, "102", comments[1].ID)
	assert.Equal(t, "bob", comments[1].Author)
	assert.Equal(t, "Second comment", comments[1].Body)
}

func TestDoRequest_authHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ghp_secret_token", r.Header.Get("Authorization"))

		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_secret_token")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "octocat/hello-world",
		MaxResults: 10,
	})

	require.NoError(t, err)
}

func TestDeleteIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues/42", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var payload map[string]string
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "closed", payload["state"])

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"number":42,"state":"closed"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.DeleteIssue(context.Background(), "octocat/hello-world#42")

	require.NoError(t, err)
}

func TestDeleteIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.DeleteIssue(context.Background(), "octocat/hello-world#42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestDeleteIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	err := client.DeleteIssue(context.Background(), "badkey")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestListComments_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	comments, err := client.ListComments(context.Background(), "octocat/hello-world#42")

	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestTransitionIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues/1", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload map[string]string
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "closed", payload["state"])

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.TransitionIssue(context.Background(), "octocat/hello-world#1", "closed")
	require.NoError(t, err)
}

func TestTransitionIssue_invalidState(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	err := client.TransitionIssue(context.Background(), "octocat/hello-world#1", "In Progress")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitHub only supports")
}

func TestListStatuses_github(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	statuses, err := client.ListStatuses(context.Background(), "octocat/hello-world#1")
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, "open", statuses[0].Name)
	assert.Equal(t, "closed", statuses[1].Name)
}

func TestAssignIssue_happy(t *testing.T) {
	var gotBody map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/issues/1", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"number":1}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.AssignIssue(context.Background(), "octocat/hello-world#1", "octocat")

	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"assignees": {"octocat"}}, gotBody)
}

func TestAssignIssue_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.AssignIssue(context.Background(), "octocat/hello-world#1", "octocat")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetCurrentUser_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/user", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"login":"octocat"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	login, err := client.GetCurrentUser(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "octocat", login)
}

func TestGetCurrentUser_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.GetCurrentUser(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestEditIssue_happy(t *testing.T) {
	title := "Updated Title"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/repos/octocat/repo/issues/1", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var got map[string]string
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "Updated Title", got["title"])

		_, _ = fmt.Fprint(w, `{"number":1,"title":"Updated Title","body":"desc","state":"open"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issue, err := client.EditIssue(context.Background(), "octocat/repo#1", tracker.EditOptions{Title: &title})

	require.NoError(t, err)
	assert.Equal(t, "octocat/repo#1", issue.Key)
	assert.Equal(t, "Updated Title", issue.Title)
}

func TestEditIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	title := "X"
	client := New(srv.URL, "ghp_test")
	_, err := client.EditIssue(context.Background(), "octocat/repo#1", tracker.EditOptions{Title: &title})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestSetHTTPDoer_github(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	client.SetHTTPDoer(http.DefaultClient)
	// No panic means success; SetHTTPDoer is a simple setter.
}

func TestListIssues_allRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/search/issues", r.URL.Path)
		assert.Contains(t, r.URL.Query().Get("q"), "is:issue")
		assert.Contains(t, r.URL.Query().Get("q"), "is:open")
		assert.Equal(t, "30", r.URL.Query().Get("per_page"))
		assert.Equal(t, "created", r.URL.Query().Get("sort"))
		assert.Equal(t, "desc", r.URL.Query().Get("order"))

		_, _ = fmt.Fprint(w, `{
			"items": [
				{
					"number": 10,
					"html_url": "https://github.com/octocat/repo-a/issues/10",
					"title": "Issue in repo A",
					"body": "body A",
					"state": "open",
					"user": {"login": "alice"},
					"labels": [{"name": "bug"}],
					"repository_url": "https://api.github.com/repos/octocat/repo-a"
				},
				{
					"number": 20,
					"html_url": "https://github.com/other/repo-b/issues/20",
					"title": "Issue in repo B",
					"body": "body B",
					"state": "open",
					"user": {"login": "bob"},
					"labels": [],
					"repository_url": "https://api.github.com/repos/other/repo-b"
				},
				{
					"number": 30,
					"html_url": "https://github.com/octocat/repo-a/pull/30",
					"title": "A pull request",
					"body": "pr body",
					"state": "open",
					"user": {"login": "charlie"},
					"labels": [],
					"pull_request": {},
					"repository_url": "https://api.github.com/repos/octocat/repo-a"
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "", // empty project triggers listAllIssues
		MaxResults: 30,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2, "PRs should be filtered out")

	assert.Equal(t, "octocat/repo-a#10", issues[0].Key)
	assert.Equal(t, "octocat/repo-a", issues[0].Project)
	assert.Equal(t, "Issue in repo A", issues[0].Title)
	assert.Equal(t, "bug", issues[0].Type)
	assert.Equal(t, "alice", issues[0].Reporter)

	assert.Equal(t, "other/repo-b#20", issues[1].Key)
	assert.Equal(t, "other/repo-b", issues[1].Project)
	assert.Equal(t, "Issue in repo B", issues[1].Title)
	assert.Equal(t, "", issues[1].Type) // no labels
	assert.Equal(t, "bob", issues[1].Reporter)
}

func TestListIssues_allRepos_includeAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		assert.Contains(t, q, "is:issue")
		assert.NotContains(t, q, "is:open") // IncludeAll should omit "is:open"

		_, _ = fmt.Fprint(w, `{"items": []}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "",
		MaxResults: 10,
		IncludeAll: true,
	})

	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestListIssues_allRepos_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "standard API URL",
			url:       "https://api.github.com/repos/octocat/hello-world",
			wantOwner: "octocat",
			wantRepo:  "hello-world",
		},
		{
			name:      "enterprise API URL",
			url:       "https://github.example.com/api/v3/repos/myorg/myrepo",
			wantOwner: "myorg",
			wantRepo:  "myrepo",
		},
		{
			name:      "no repos prefix",
			url:       "https://api.github.com/users/octocat",
			wantOwner: "",
			wantRepo:  "",
		},
		{
			name:      "repos but no repo name",
			url:       "https://api.github.com/repos/octocat",
			wantOwner: "",
			wantRepo:  "",
		},
		{
			name:      "empty string",
			url:       "",
			wantOwner: "",
			wantRepo:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo := parseRepoURL(tt.url)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
		})
	}
}

func TestListComments_invalidKey(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	_, err := client.ListComments(context.Background(), "badkey")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestListComments_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.ListComments(context.Background(), "octocat/hello-world#42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestTransitionIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	err := client.TransitionIssue(context.Background(), "badkey", "closed")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestTransitionIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.TransitionIssue(context.Background(), "octocat/hello-world#1", "open")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAssignIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	err := client.AssignIssue(context.Background(), "badkey", "octocat")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestGetCurrentUser_invalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `not json`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.GetCurrentUser(context.Background())

	require.Error(t, err)
}

func TestEditIssue_invalidKey(t *testing.T) {
	title := "X"
	client := New("http://localhost", "ghp_test")
	_, err := client.EditIssue(context.Background(), "badkey", tracker.EditOptions{Title: &title})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid issue key format")
}

func TestEditIssue_withDescription(t *testing.T) {
	title := "New Title"
	desc := "New Body"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var got map[string]string
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "New Title", got["title"])
		assert.Equal(t, "New Body", got["body"])

		_, _ = fmt.Fprint(w, `{"number":5,"title":"New Title","body":"New Body","state":"open"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issue, err := client.EditIssue(context.Background(), "octocat/repo#5", tracker.EditOptions{
		Title:       &title,
		Description: &desc,
	})

	require.NoError(t, err)
	assert.Equal(t, "New Title", issue.Title)
	assert.Equal(t, "New Body", issue.Description)
}

func TestDeleteIssue_stateNotClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a workflow rule re-opening the issue.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"number":42,"state":"open"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.DeleteIssue(context.Background(), "octocat/hello-world#42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not transition to closed")
}

func TestCreateIssue_invalidProject(t *testing.T) {
	client := New("http://localhost", "ghp_test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "noslash",
		Title:   "Will fail",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid project format")
}

func TestListComments_commentWithoutUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[
			{"id": 201, "body": "Anonymous comment", "user": null, "created_at": "2025-03-01T12:00:00Z"}
		]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	comments, err := client.ListComments(context.Background(), "octocat/hello-world#1")

	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "", comments[0].Author)
	assert.Equal(t, "Anonymous comment", comments[0].Body)
}

func TestListIssues_withUpdatedSince(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since := r.URL.Query().Get("since")
		assert.NotEmpty(t, since, "since parameter should be set")

		_, _ = fmt.Fprint(w, `[
			{"number":1,"title":"Recent issue","body":"","state":"open","user":{"login":"alice"},"labels":[],"updated_at":"2025-06-01T00:00:00Z"}
		]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	since, _ := time.Parse(time.RFC3339, "2025-05-01T00:00:00Z")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:      "octocat/hello-world",
		MaxResults:   10,
		UpdatedSince: since,
	})

	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "octocat/hello-world#1", issues[0].Key)
}

func TestAddComment_commentWithoutUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{
			"id": 301,
			"body": "Bot comment",
			"user": null,
			"created_at": "2025-02-01T09:00:00Z"
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	comment, err := client.AddComment(context.Background(), "octocat/hello-world#1", "Bot comment")

	require.NoError(t, err)
	assert.Equal(t, "301", comment.ID)
	assert.Equal(t, "", comment.Author)
	assert.Equal(t, "Bot comment", comment.Body)
}

func TestTransitionIssue_caseInsensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload map[string]string
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "open", payload["state"])

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.TransitionIssue(context.Background(), "octocat/hello-world#1", "Open")
	require.NoError(t, err)
}

func TestCreateIssue_withParent(t *testing.T) {
	var subIssueBody map[string]int
	createCalled, subCalled := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octocat/hello-world/issues":
			createCalled = true
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":555,"number":99,"title":"Child","body":""}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octocat/hello-world/issues/7/sub_issues":
			subCalled = true
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &subIssueBody))
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "octocat/hello-world",
		Title:     "Child",
		ParentKey: "octocat/hello-world#7",
	})
	require.NoError(t, err)
	assert.True(t, createCalled, "create endpoint should be called")
	assert.True(t, subCalled, "sub_issues endpoint should be called")
	assert.Equal(t, 555, subIssueBody["sub_issue_id"]) // child's internal id, not its number
	assert.Equal(t, "octocat/hello-world#99", issue.Key)
	assert.Equal(t, "octocat/hello-world#7", issue.ParentKey)
}

func TestCreateIssue_invalidParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":555,"number":99,"title":"Child","body":""}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "octocat/hello-world",
		Title:     "Child",
		ParentKey: "nohash",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid parent key")
}
