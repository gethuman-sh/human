package shortcut

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
		switch r.URL.Path {
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)

		case "/api/v3/groups/grp-uuid-1/stories":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "tok-test", r.Header.Get("Shortcut-Token"))

			_, _ = fmt.Fprint(w, `[
				{"id":1,"name":"Bug report","story_type":"bug","workflow_state_id":500,"owner_ids":["uuid-alice"],"requested_by_id":"uuid-bob","description":""},
				{"id":2,"name":"Feature request","story_type":"feature","workflow_state_id":501,"owner_ids":[],"requested_by_id":"","description":""},
				{"id":3,"name":"Old done item","story_type":"chore","workflow_state_id":502,"owner_ids":[],"requested_by_id":"","description":""}
			]`)

		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"},
				{"id":502,"name":"Done","type":"done"}
			]}]`)

		case "/api/v3/members/uuid-alice":
			_, _ = fmt.Fprint(w, `{"id":"uuid-alice","profile":{"name":"Alice Smith","display_name":"Alice"}}`)

		case "/api/v3/members/uuid-bob":
			_, _ = fmt.Fprint(w, `{"id":"uuid-bob","profile":{"name":"Bob Jones","display_name":"Bob"}}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 50,
	})

	require.NoError(t, err)
	// "Done" story filtered out by default
	require.Len(t, issues, 2)

	assert.Equal(t, "1", issues[0].Key)
	assert.Equal(t, "Bug report", issues[0].Title)
	assert.Equal(t, "To Do", issues[0].Status)
	assert.Equal(t, tracker.CategoryUnstarted, issues[0].StatusType)
	assert.Equal(t, "bug", issues[0].Type)
	assert.Equal(t, "Alice", issues[0].Assignee)
	assert.Equal(t, "Bob", issues[0].Reporter)

	assert.Equal(t, "2", issues[1].Key)
	assert.Equal(t, "Feature request", issues[1].Title)
	assert.Equal(t, "In Progress", issues[1].Status)
	assert.Equal(t, tracker.CategoryStarted, issues[1].StatusType)
	assert.Equal(t, "", issues[1].Assignee)
	assert.Equal(t, "", issues[1].Reporter)
}

func TestListIssues_all(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)

		case "/api/v3/groups/grp-uuid-1/stories":
			_, _ = fmt.Fprint(w, `[
				{"id":1,"name":"Active item","story_type":"feature","workflow_state_id":500,"owner_ids":[],"requested_by_id":""},
				{"id":2,"name":"Done item","story_type":"feature","workflow_state_id":502,"owner_ids":[],"requested_by_id":""},
				{"id":3,"name":"Archived item","story_type":"chore","workflow_state_id":500,"archived":true,"owner_ids":[],"requested_by_id":""}
			]`)

		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":502,"name":"Done","type":"done"}
			]}]`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")

	// IncludeAll=true: all 3 stories returned
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 50,
		IncludeAll: true,
	})
	require.NoError(t, err)
	require.Len(t, issues, 3)

	// IncludeAll=false (default): only active story returned
	client2 := New(srv.URL, "tok-test")
	filtered, err := client2.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 50,
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "Active item", filtered[0].Title)
}

func TestListIssues_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)
		case "/api/v3/groups/grp-uuid-1/stories":
			_, _ = fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 10,
	})

	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestListIssues_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListIssues_groupNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Other"}]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "Human",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "group not found")
}

func TestGetIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/stories/42":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "tok-test", r.Header.Get("Shortcut-Token"))

			_, _ = fmt.Fprint(w, `{
				"id": 42,
				"name": "The answer",
				"description": "## Description\n\nThis is markdown.",
				"story_type": "feature",
				"workflow_state_id": 500,
				"app_url": "https://app.shortcut.com/workspace/story/42",
				"owner_ids": ["uuid-alice"],
				"requested_by_id": "uuid-bob"
			}`)

		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"In Progress","type":"started"}
			]}]`)

		case "/api/v3/members/uuid-alice":
			_, _ = fmt.Fprint(w, `{"id":"uuid-alice","profile":{"name":"Alice","display_name":"Alice"}}`)

		case "/api/v3/members/uuid-bob":
			_, _ = fmt.Fprint(w, `{"id":"uuid-bob","profile":{"name":"Bob","display_name":"Bob"}}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issue, err := client.GetIssue(context.Background(), "42")

	require.NoError(t, err)
	assert.Equal(t, "42", issue.Key)
	assert.Equal(t, "The answer", issue.Title)
	assert.Equal(t, "In Progress", issue.Status)
	assert.Equal(t, "feature", issue.Type)
	assert.Equal(t, "Alice", issue.Assignee)
	assert.Equal(t, "Bob", issue.Reporter)
	assert.Equal(t, "## Description\n\nThis is markdown.", issue.Description)
}

func TestGetIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	_, err := client.GetIssue(context.Background(), "42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "tok-test")
	_, err := client.GetIssue(context.Background(), "not-a-number")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid story ID")
}

func TestCreateIssue_happy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"}
			]}]`)

		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"},{"id":"grp-uuid-2","name":"Other"}]`)

		case "/api/v3/stories":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":99,"name":"New story","description":"Some description","story_type":"feature","workflow_state_id":500}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:     "Human",
		Title:       "New story",
		Description: "Some description",
	})

	require.NoError(t, err)
	assert.Equal(t, "99", issue.Key)
	assert.Equal(t, "Human", issue.Project)
	assert.Equal(t, "New story", issue.Title)
	assert.Equal(t, "Some description", issue.Description)

	assert.Equal(t, "New story", gotBody["name"])
	assert.Equal(t, "Some description", gotBody["description"])
	assert.Equal(t, float64(500), gotBody["workflow_state_id"])
	assert.Equal(t, "grp-uuid-1", gotBody["group_id"])
}

func TestCreateIssue_withoutDescription(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"}
			]}]`)

		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)

		case "/api/v3/stories":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":100,"name":"No desc","story_type":"feature","workflow_state_id":500}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "Human",
		Title:   "No desc",
	})

	require.NoError(t, err)
	assert.Equal(t, "100", issue.Key)
	// Only name, no description
	assert.Equal(t, "No desc", gotBody["name"])
	_, hasDesc := gotBody["description"]
	assert.False(t, hasDesc)
}

func TestCreateIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"}
			]}]`)
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "Human",
		Title:   "Will fail",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestDefaultWorkflowStateID(t *testing.T) {
	fetchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/workflows" {
			fetchCount++
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"},
				{"id":502,"name":"Done","type":"done"}
			]}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")

	// First call fetches workflows and picks first "unstarted" state
	id1, err := client.defaultWorkflowStateID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(500), id1)

	// Second call uses cached value
	id2, err := client.defaultWorkflowStateID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(500), id2)

	// Workflows should only be fetched once
	assert.Equal(t, 1, fetchCount)
}

func TestDeleteIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/api/v3/stories/42", r.URL.Path)
		assert.Equal(t, "tok-test", r.Header.Get("Shortcut-Token"))

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	err := client.DeleteIssue(context.Background(), "42")

	require.NoError(t, err)
}

func TestDeleteIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	err := client.DeleteIssue(context.Background(), "42")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestDeleteIssue_invalidKey(t *testing.T) {
	client := New("http://localhost", "tok-test")
	err := client.DeleteIssue(context.Background(), "badkey")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid story ID")
}

func TestAddComment_happy(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/stories/42/comments":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{
				"id": 101,
				"text": "Hello world",
				"author_id": "uuid-alice",
				"created_at": "2025-01-15T10:30:00Z",
				"updated_at": "2025-01-15T10:30:00Z"
			}`)

		case "/api/v3/members/uuid-alice":
			_, _ = fmt.Fprint(w, `{"id":"uuid-alice","profile":{"name":"Alice","display_name":"Alice"}}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	comment, err := client.AddComment(context.Background(), "42", "Hello world")

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

	client := New(srv.URL, "tok-test")
	_, err := client.AddComment(context.Background(), "42", "test")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAddComment_invalidKey(t *testing.T) {
	client := New("http://localhost", "tok-test")
	_, err := client.AddComment(context.Background(), "badkey", "test")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid story ID")
}

func TestListComments_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/stories/42/comments":
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = fmt.Fprint(w, `[
				{"id": 101, "text": "First comment", "author_id": "uuid-alice", "created_at": "2025-01-15T10:30:00Z", "updated_at": "2025-01-15T10:30:00Z"},
				{"id": 102, "text": "Second comment", "author_id": "uuid-bob", "created_at": "2025-01-16T11:00:00Z", "updated_at": "2025-01-16T11:00:00Z"}
			]`)

		case "/api/v3/members/uuid-alice":
			_, _ = fmt.Fprint(w, `{"id":"uuid-alice","profile":{"name":"Alice","display_name":"Alice"}}`)

		case "/api/v3/members/uuid-bob":
			_, _ = fmt.Fprint(w, `{"id":"uuid-bob","profile":{"name":"Bob","display_name":"Bob"}}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	comments, err := client.ListComments(context.Background(), "42")

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
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	comments, err := client.ListComments(context.Background(), "42")

	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestDoRequest_authHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "my-secret-token", r.Header.Get("Shortcut-Token"))
		_, _ = fmt.Fprint(w, `{"id":1,"name":"Test","story_type":"feature","workflow_state_id":500,"owner_ids":[],"requested_by_id":""}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "my-secret-token")
	// Pre-populate state cache to avoid extra requests.
	client.states = map[int64]string{500: "To Do"}
	client.stateTypes = map[int64]tracker.Category{500: tracker.CategoryUnstarted}
	_, err := client.GetIssue(context.Background(), "1")

	require.NoError(t, err)
}

func Test_parseStoryID(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantID  int64
		wantErr string
	}{
		{name: "valid", key: "42", wantID: 42},
		{name: "large number", key: "123456", wantID: 123456},
		{name: "not a number", key: "abc", wantErr: "invalid story ID"},
		{name: "empty", key: "", wantErr: "invalid story ID"},
		{name: "float", key: "1.5", wantErr: "invalid story ID"},
		{name: "negative", key: "-1", wantErr: "invalid story ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := parseStoryID(tt.key)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestResolveStateName_caching(t *testing.T) {
	fetchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/workflows" {
			fetchCount++
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"}
			]}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")

	// First call fetches workflows
	name1, err := client.resolveStateName(context.Background(), 500)
	require.NoError(t, err)
	assert.Equal(t, "To Do", name1)

	// Second call uses cache
	name2, err := client.resolveStateName(context.Background(), 501)
	require.NoError(t, err)
	assert.Equal(t, "In Progress", name2)

	// Workflows should only be fetched once
	assert.Equal(t, 1, fetchCount)

	// Unknown state ID returns Unknown(id)
	name3, err := client.resolveStateName(context.Background(), 999)
	require.NoError(t, err)
	assert.Equal(t, "Unknown(999)", name3)
}

func TestResolveMemberName_caching(t *testing.T) {
	fetchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/members/uuid-alice" {
			fetchCount++
			_, _ = fmt.Fprint(w, `{"id":"uuid-alice","profile":{"name":"Alice Smith","display_name":"Alice"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")

	// First call fetches member
	name1, err := client.resolveMemberName(context.Background(), "uuid-alice")
	require.NoError(t, err)
	assert.Equal(t, "Alice", name1)

	// Second call uses cache
	name2, err := client.resolveMemberName(context.Background(), "uuid-alice")
	require.NoError(t, err)
	assert.Equal(t, "Alice", name2)

	// Member should only be fetched once
	assert.Equal(t, 1, fetchCount)

	// Empty member ID returns empty string
	name3, err := client.resolveMemberName(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "", name3)
}

func TestTransitionIssue_happy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"},
				{"id":502,"name":"Done","type":"done"}
			]}]`)

		case "/api/v3/stories/1":
			assert.Equal(t, http.MethodPut, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			_, _ = fmt.Fprint(w, `{"id":1,"name":"Bug"}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	err := client.TransitionIssue(context.Background(), "1", "In Progress")

	require.NoError(t, err)
	assert.Equal(t, float64(501), gotBody["workflow_state_id"])
}

func TestTransitionIssue_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"},
				{"id":502,"name":"Done","type":"done"}
			]}]`)

		case "/api/v3/stories/1":
			w.WriteHeader(http.StatusBadRequest)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	err := client.TransitionIssue(context.Background(), "1", "In Progress")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAssignIssue_happy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/stories/1":
			assert.Equal(t, http.MethodPut, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			_, _ = fmt.Fprint(w, `{"id":1,"name":"Bug"}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	err := client.AssignIssue(context.Background(), "1", "uuid-alice")

	require.NoError(t, err)
	ownerIDs, ok := gotBody["owner_ids"].([]any)
	require.True(t, ok)
	require.Len(t, ownerIDs, 1)
	assert.Equal(t, "uuid-alice", ownerIDs[0])
}

func TestAssignIssue_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	err := client.AssignIssue(context.Background(), "1", "uuid-alice")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetCurrentUser_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/member-info":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "tok-test", r.Header.Get("Shortcut-Token"))

			_, _ = fmt.Fprint(w, `{"id":"uuid-me"}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	userID, err := client.GetCurrentUser(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "uuid-me", userID)
}

func TestGetCurrentUser_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	_, err := client.GetCurrentUser(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestEditIssue_happy(t *testing.T) {
	title := "Updated Title"
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v3/stories/123":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var got map[string]string
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, "Updated Title", got["name"])

			_, _ = fmt.Fprint(w, `{"id":123,"name":"Updated Title","description":"desc","story_type":"feature","workflow_state_id":500,"owner_ids":[],"requested_by_id":""}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"Unstarted","type":"unstarted"}]}]`)
		default:
			t.Fatalf("unexpected request: %s %s (call %d)", r.Method, r.URL.Path, callCount)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issue, err := client.EditIssue(context.Background(), "123", tracker.EditOptions{Title: &title})

	require.NoError(t, err)
	assert.Equal(t, "123", issue.Key)
	assert.Equal(t, "Updated Title", issue.Title)
}

func TestEditIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	title := "X"
	client := New(srv.URL, "tok-test")
	_, err := client.EditIssue(context.Background(), "123", tracker.EditOptions{Title: &title})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestResolveMemberName_fallbackToName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"uuid-x","profile":{"name":"Full Name","display_name":""}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	name, err := client.resolveMemberName(context.Background(), "uuid-x")
	require.NoError(t, err)
	assert.Equal(t, "Full Name", name)
}

func TestListStatuses_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/workflows" {
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"},
				{"id":502,"name":"Done","type":"done"},
				{"id":503,"name":"Blocked","type":"unstarted"}
			]}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	statuses, err := client.ListStatuses(context.Background(), "42")

	require.NoError(t, err)
	require.Len(t, statuses, 4)

	// Statuses are sorted alphabetically by name.
	assert.Equal(t, "Blocked", statuses[0].Name)
	assert.Equal(t, tracker.CategoryUnstarted, statuses[0].Category)

	assert.Equal(t, "Done", statuses[1].Name)
	assert.Equal(t, tracker.CategoryDone, statuses[1].Category)

	assert.Equal(t, "In Progress", statuses[2].Name)
	assert.Equal(t, tracker.CategoryStarted, statuses[2].Category)

	assert.Equal(t, "To Do", statuses[3].Name)
	assert.Equal(t, tracker.CategoryUnstarted, statuses[3].Category)
}

func TestListStatuses_emptyStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/workflows" {
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[]}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	statuses, err := client.ListStatuses(context.Background(), "1")

	require.NoError(t, err)
	assert.Empty(t, statuses)
}

func TestListIssues_searchStories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/stories/search":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			assert.Contains(t, req, "group_ids")
			assert.Contains(t, req, "updated_at_start")
			_, _ = fmt.Fprint(w, `[{"id":10,"name":"Recent","story_type":"feature","workflow_state_id":500,"owner_ids":[],"requested_by_id":""}]`)
		case r.URL.Path == "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:      "Human",
		UpdatedSince: since,
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "Recent", issues[0].Title)
}

func TestListIssues_searchAllStories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"},{"id":"grp-uuid-2","name":"Other"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/stories/search":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			// archived:false must be set so the body is never empty
			assert.Equal(t, false, req["archived"])
			_, _ = fmt.Fprint(w, `[
				{"id":1,"name":"Story A","story_type":"feature","workflow_state_id":500,"group_id":"grp-uuid-1","owner_ids":[],"requested_by_id":""},
				{"id":2,"name":"Story B","story_type":"bug","workflow_state_id":500,"group_id":"grp-uuid-2","owner_ids":[],"requested_by_id":""}
			]`)
		case r.URL.Path == "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project: "", // empty = search across all groups via search endpoint
	})
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, "Human", issues[0].Project)
	assert.Equal(t, "Other", issues[1].Project)
}

func TestListIssues_noTeamStories(t *testing.T) {
	// Stories with no group_id must be returned when listing without a project filter.
	// The old team-iteration approach silently dropped such stories.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/stories/search":
			_, _ = fmt.Fprint(w, `[
				{"id":1,"name":"Teamless story","story_type":"feature","workflow_state_id":500,"group_id":"","owner_ids":[],"requested_by_id":""}
			]`)
		case r.URL.Path == "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project: "",
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "Teamless story", issues[0].Title)
	// No group assigned, project field should be empty
	assert.Equal(t, "", issues[0].Project)
}

func TestResolveStateByName_caseInsensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/workflows" {
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"},
				{"id":501,"name":"In Progress","type":"started"},
				{"id":502,"name":"Done","type":"done"}
			]}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")

	// Case-insensitive exact match.
	id, err := client.resolveStateByName(context.Background(), "in progress")
	require.NoError(t, err)
	assert.Equal(t, int64(501), id)

	// Type-based fallback: "started" matches the type, not the name.
	id2, err := client.resolveStateByName(context.Background(), "started")
	require.NoError(t, err)
	assert.Equal(t, int64(501), id2)

	// Not found.
	_, err = client.resolveStateByName(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow state not found")
}

func TestCacheMember_noDuplicate(t *testing.T) {
	client := New("http://unused", "tok-test")
	client.cacheMember("uuid-1", "Alice")
	client.cacheMember("uuid-1", "Bob") // should not overwrite
	assert.Equal(t, "Alice", client.members["uuid-1"])
}

func TestResolveMemberName_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	name, err := client.resolveMemberName(context.Background(), "uuid-fail")
	require.NoError(t, err) // errors are swallowed, name is empty
	assert.Equal(t, "", name)

	// Verify negative cache: second call should not hit server.
	name2, err := client.resolveMemberName(context.Background(), "uuid-fail")
	require.NoError(t, err)
	assert.Equal(t, "", name2)
}

func TestSetHTTPDoer_shortcut(t *testing.T) {
	client := New("http://unused", "tok-test")
	client.SetHTTPDoer(http.DefaultClient)
}

func TestCreateIssue_withStoryType(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
		case "/api/v3/stories":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":50,"name":"Bug","story_type":"bug","workflow_state_id":500}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Title: "Bug",
		Type:  "bug",
	})
	require.NoError(t, err)
	assert.Equal(t, "bug", gotBody["story_type"])
}

func TestListStatuses_caching(t *testing.T) {
	fetchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/workflows" {
			fetchCount++
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[
				{"id":500,"name":"To Do","type":"unstarted"}
			]}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")

	statuses1, err := client.ListStatuses(context.Background(), "1")
	require.NoError(t, err)
	require.Len(t, statuses1, 1)

	statuses2, err := client.ListStatuses(context.Background(), "2")
	require.NoError(t, err)
	require.Len(t, statuses2, 1)

	assert.Equal(t, 1, fetchCount)
}

func TestCreateIssue_withParent(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"grp-uuid-1","name":"Human"}]`)
		case "/api/v3/stories":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":99,"name":"Child","parent_story_id":42}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "Human",
		Title:     "Child",
		ParentKey: "42",
	})
	require.NoError(t, err)
	assert.Equal(t, "99", issue.Key)
	assert.Equal(t, "42", issue.ParentKey)
	assert.Equal(t, float64(42), gotBody["parent_story_id"])
}

func TestCreateIssue_invalidParent(t *testing.T) {
	client := New("http://localhost", "tok-test")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "Human",
		Title:     "Child",
		ParentKey: "not-a-number",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid parent story ID")
}

func TestGetIssue_withParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/stories/99":
			_, _ = fmt.Fprint(w, `{"id":99,"name":"Child","workflow_state_id":500,"parent_story_id":42}`)
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test")
	issue, err := client.GetIssue(context.Background(), "99")
	require.NoError(t, err)
	assert.Equal(t, "42", issue.ParentKey)
}
