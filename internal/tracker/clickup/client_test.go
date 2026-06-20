package clickup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gethuman-sh/human/internal/tracker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListIssues_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/list/901/task":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "tok-test", r.Header.Get("Authorization"))
			assert.Equal(t, "0", r.URL.Query().Get("page"))

			_, _ = fmt.Fprint(w, `{
				"tasks": [
					{
						"id": "abc1",
						"name": "Bug report",
						"description": "Fix the bug",
						"status": {"status": "open", "type": "open"},
						"assignees": [{"id": 100, "username": "alice", "email": "alice@example.com"}],
						"creator": {"id": 200, "username": "bob", "email": "bob@example.com"},
						"date_created": "1700000000000",
						"date_updated": "1700100000000",
						"url": "https://app.clickup.com/t/abc1",
						"list": {"id": "901", "name": "Sprint 1"}
					},
					{
						"id": "abc2",
						"name": "Feature request",
						"description": "Add feature",
						"status": {"status": "in progress", "type": "custom"},
						"assignees": [],
						"creator": {"id": 200, "username": "bob", "email": "bob@example.com"},
						"date_created": "1700000000000",
						"date_updated": "1700100000000",
						"url": "https://app.clickup.com/t/abc2",
						"list": {"id": "901", "name": "Sprint 1"}
					},
					{
						"id": "abc3",
						"name": "Old done item",
						"description": "",
						"status": {"status": "complete", "type": "done"},
						"assignees": [],
						"creator": {"id": 200, "username": "bob", "email": "bob@example.com"},
						"date_created": "1700000000000",
						"date_updated": "1700100000000",
						"url": "https://app.clickup.com/t/abc3",
						"list": {"id": "901", "name": "Sprint 1"}
					}
				],
				"last_page": true
			}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "901",
		MaxResults: 50,
	})

	require.NoError(t, err)
	// "done" task filtered out by default
	require.Len(t, issues, 2)

	assert.Equal(t, "abc1", issues[0].Key)
	assert.Equal(t, "Bug report", issues[0].Title)
	assert.Equal(t, "open", issues[0].Status)
	assert.Equal(t, tracker.CategoryUnstarted, issues[0].StatusType)
	assert.Equal(t, "alice", issues[0].Assignee)
	assert.Equal(t, "bob", issues[0].Reporter)

	assert.Equal(t, "abc2", issues[1].Key)
	assert.Equal(t, "Feature request", issues[1].Title)
	assert.Equal(t, "in progress", issues[1].Status)
	assert.Equal(t, tracker.CategoryStarted, issues[1].StatusType)
	assert.Equal(t, "", issues[1].Assignee)
	assert.Equal(t, "bob", issues[1].Reporter)
}

func TestListIssues_all(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/list/901/task":
			_, _ = fmt.Fprint(w, `{
				"tasks": [
					{"id":"abc1","name":"Active item","status":{"status":"open","type":"open"},"assignees":[],"creator":{},"list":{"id":"901"}},
					{"id":"abc2","name":"Done item","status":{"status":"complete","type":"done"},"assignees":[],"creator":{},"list":{"id":"901"}},
					{"id":"abc3","name":"Closed item","status":{"status":"closed","type":"closed"},"assignees":[],"creator":{},"list":{"id":"901"}}
				],
				"last_page": true
			}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")

	// IncludeAll=true: all 3 tasks returned
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "901",
		MaxResults: 50,
		IncludeAll: true,
	})
	require.NoError(t, err)
	require.Len(t, issues, 3)

	// IncludeAll=false (default): only active task returned
	client2 := New(srv.URL, "tok-test", "")
	filtered, err := client2.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "901",
		MaxResults: 50,
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "Active item", filtered[0].Title)
}

func TestListIssues_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/list/901/task":
			_, _ = fmt.Fprint(w, `{"tasks":[],"last_page":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "901",
		MaxResults: 10,
	})

	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestListIssues_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "901",
		MaxResults: 10,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListIssues_pagination(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/list/901/task":
			page := r.URL.Query().Get("page")
			callCount++
			switch page {
			case "0":
				_, _ = fmt.Fprint(w, `{
					"tasks": [{"id":"abc1","name":"Task 1","status":{"status":"open","type":"open"},"assignees":[],"creator":{},"list":{"id":"901"}}],
					"last_page": false
				}`)
			case "1":
				_, _ = fmt.Fprint(w, `{
					"tasks": [{"id":"abc2","name":"Task 2","status":{"status":"open","type":"open"},"assignees":[],"creator":{},"list":{"id":"901"}}],
					"last_page": true
				}`)
			default:
				t.Errorf("unexpected page: %s", page)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	issues, err := client.ListIssues(context.Background(), tracker.ListOptions{
		Project:    "901",
		MaxResults: 50,
	})

	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, "Task 1", issues[0].Title)
	assert.Equal(t, "Task 2", issues[1].Title)
	assert.Equal(t, 2, callCount)
}

func TestGetIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/task/abc123":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "tok-test", r.Header.Get("Authorization"))

			_, _ = fmt.Fprint(w, `{
				"id": "abc123",
				"name": "The answer",
				"description": "## Description\n\nThis is markdown.",
				"status": {"status": "in progress", "type": "custom"},
				"assignees": [{"id": 100, "username": "alice", "email": "alice@example.com"}],
				"creator": {"id": 200, "username": "bob", "email": "bob@example.com"},
				"date_created": "1700000000000",
				"date_updated": "1700100000000",
				"url": "https://app.clickup.com/t/abc123",
				"list": {"id": "901", "name": "Sprint 1"}
			}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	issue, err := client.GetIssue(context.Background(), "abc123")

	require.NoError(t, err)
	assert.Equal(t, "abc123", issue.Key)
	assert.Equal(t, "The answer", issue.Title)
	assert.Equal(t, "in progress", issue.Status)
	assert.Equal(t, tracker.CategoryStarted, issue.StatusType)
	assert.Equal(t, "alice", issue.Assignee)
	assert.Equal(t, "bob", issue.Reporter)
	assert.Equal(t, "## Description\n\nThis is markdown.", issue.Description)
}

func TestGetIssue_customID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/task/PROJ-42":
			assert.Equal(t, "true", r.URL.Query().Get("custom_task_ids"))
			assert.Equal(t, "9876", r.URL.Query().Get("team_id"))

			_, _ = fmt.Fprint(w, `{
				"id": "abc456",
				"custom_id": "PROJ-42",
				"name": "Custom ID task",
				"description": "",
				"status": {"status": "open", "type": "open"},
				"assignees": [],
				"creator": {},
				"list": {"id": "901"}
			}`)

		default:
			t.Errorf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "9876")
	issue, err := client.GetIssue(context.Background(), "PROJ-42")

	require.NoError(t, err)
	assert.Equal(t, "abc456", issue.Key)
	assert.Equal(t, "Custom ID task", issue.Title)
}

func TestCreateIssue_happy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/list/901/task":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{
				"id": "xyz999",
				"name": "New task",
				"description": "Some description",
				"status": {"status": "open", "type": "open"},
				"url": "https://app.clickup.com/t/xyz999",
				"list": {"id": "901"}
			}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:     "901",
		Title:       "New task",
		Description: "Some description",
	})

	require.NoError(t, err)
	assert.Equal(t, "xyz999", issue.Key)
	assert.Equal(t, "901", issue.Project)
	assert.Equal(t, "New task", issue.Title)
	assert.Equal(t, "Some description", issue.Description)

	assert.Equal(t, "New task", gotBody["name"])
	assert.Equal(t, "Some description", gotBody["description"])
}

func TestDeleteIssue_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/api/v2/task/abc123", r.URL.Path)
		assert.Equal(t, "tok-test", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.DeleteIssue(context.Background(), "abc123")

	require.NoError(t, err)
}

func TestTransitionIssue_happy(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/v2/task/abc123", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		_, _ = fmt.Fprint(w, `{"id":"abc123","name":"Task","status":{"status":"in progress","type":"custom"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.TransitionIssue(context.Background(), "abc123", "in progress")

	require.NoError(t, err)
	assert.Equal(t, "in progress", gotBody["status"])
}

func TestAssignIssue_happy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/v2/task/abc123", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		_, _ = fmt.Fprint(w, `{"id":"abc123","name":"Task","status":{"status":"open","type":"open"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.AssignIssue(context.Background(), "abc123", "12345")

	require.NoError(t, err)
	assignees := gotBody["assignees"].(map[string]any)
	addList := assignees["add"].([]any)
	assert.Equal(t, float64(12345), addList[0])
}

func TestGetCurrentUser_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v2/user", r.URL.Path)

		_, _ = fmt.Fprint(w, `{"user":{"id":42,"username":"testuser","email":"test@example.com"}}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	userID, err := client.GetCurrentUser(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "42", userID)
}

func TestEditIssue_happy(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/v2/task/abc123", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		_, _ = fmt.Fprint(w, `{
			"id": "abc123",
			"name": "Updated title",
			"description": "Updated desc",
			"status": {"status": "open", "type": "open"},
			"assignees": [],
			"creator": {},
			"list": {"id": "901"}
		}`)
	}))
	defer srv.Close()

	title := "Updated title"
	desc := "Updated desc"
	client := New(srv.URL, "tok-test", "")
	issue, err := client.EditIssue(context.Background(), "abc123", tracker.EditOptions{
		Title:       &title,
		Description: &desc,
	})

	require.NoError(t, err)
	assert.Equal(t, "abc123", issue.Key)
	assert.Equal(t, "Updated title", issue.Title)
	assert.Equal(t, "Updated desc", issue.Description)

	assert.Equal(t, "Updated title", gotBody["name"])
	assert.Equal(t, "Updated desc", gotBody["description"])
}

func TestListStatuses_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/task/abc123":
			_, _ = fmt.Fprint(w, `{
				"id": "abc123",
				"name": "Task",
				"status": {"status": "open", "type": "open"},
				"assignees": [],
				"creator": {},
				"list": {"id": "901", "name": "Sprint 1"}
			}`)
		case "/api/v2/list/901":
			_, _ = fmt.Fprint(w, `{
				"id": "901",
				"name": "Sprint 1",
				"statuses": [
					{"status": "to do", "type": "open", "orderindex": "0"},
					{"status": "in progress", "type": "custom", "orderindex": "1"},
					{"status": "review", "type": "custom", "orderindex": "2"},
					{"status": "done", "type": "done", "orderindex": "3"},
					{"status": "closed", "type": "closed", "orderindex": "4"}
				]
			}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	statuses, err := client.ListStatuses(context.Background(), "abc123")

	require.NoError(t, err)
	require.Len(t, statuses, 5)

	assert.Equal(t, "to do", statuses[0].Name)
	assert.Equal(t, tracker.CategoryUnstarted, statuses[0].Category)

	assert.Equal(t, "in progress", statuses[1].Name)
	assert.Equal(t, tracker.CategoryStarted, statuses[1].Category)

	assert.Equal(t, "review", statuses[2].Name)
	assert.Equal(t, tracker.CategoryStarted, statuses[2].Category)

	assert.Equal(t, "done", statuses[3].Name)
	assert.Equal(t, tracker.CategoryDone, statuses[3].Category)

	assert.Equal(t, "closed", statuses[4].Name)
	assert.Equal(t, tracker.CategoryDone, statuses[4].Category)
}

func TestAddComment_happy(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/task/abc123/comment":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))

			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{
				"id": "comm-1",
				"comment_text": "Hello world",
				"user": {"id": 100, "username": "alice", "email": "alice@example.com"},
				"date": "1700000000000"
			}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	comment, err := client.AddComment(context.Background(), "abc123", "Hello world")

	require.NoError(t, err)
	assert.Equal(t, "comm-1", comment.ID)
	assert.Equal(t, "alice", comment.Author)
	assert.Equal(t, "Hello world", comment.Body)
	assert.False(t, comment.Created.IsZero())

	assert.Equal(t, "Hello world", gotBody["comment_text"])
}

func TestListComments_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/task/abc123/comment":
			assert.Equal(t, http.MethodGet, r.Method)

			_, _ = fmt.Fprint(w, `{
				"comments": [
					{
						"id": "comm-1",
						"comment_text": "First comment",
						"user": {"id": 100, "username": "alice"},
						"date": "1700000000000"
					},
					{
						"id": "comm-2",
						"comment_text": "Second comment",
						"user": {"id": 200, "username": "bob"},
						"date": "1700100000000"
					}
				]
			}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	comments, err := client.ListComments(context.Background(), "abc123")

	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "comm-1", comments[0].ID)
	assert.Equal(t, "alice", comments[0].Author)
	assert.Equal(t, "First comment", comments[0].Body)
	assert.Equal(t, "comm-2", comments[1].ID)
	assert.Equal(t, "bob", comments[1].Author)
	assert.Equal(t, "Second comment", comments[1].Body)
}

func TestListIssues_requiresProject(t *testing.T) {
	client := New("http://localhost", "tok-test", "")
	_, err := client.ListIssues(context.Background(), tracker.ListOptions{
		MaxResults: 10,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project")
}

func TestLooksLikeCustomID(t *testing.T) {
	assert.True(t, looksLikeCustomID("PROJ-42"))
	assert.True(t, looksLikeCustomID("AB-1"))
	assert.False(t, looksLikeCustomID("abc123"))
	assert.False(t, looksLikeCustomID("86a2w4xxz"))
	assert.False(t, looksLikeCustomID("proj-42"))  // lowercase prefix
	assert.False(t, looksLikeCustomID("PROJ-abc")) // non-numeric suffix
	assert.False(t, looksLikeCustomID("PROJ-"))    // empty suffix
	assert.False(t, looksLikeCustomID("-42"))      // empty prefix
}

func TestMapStatusType(t *testing.T) {
	assert.Equal(t, tracker.CategoryUnstarted, mapStatusType("open"))
	assert.Equal(t, tracker.CategoryStarted, mapStatusType("custom"))
	assert.Equal(t, tracker.CategoryDone, mapStatusType("done"))
	assert.Equal(t, tracker.CategoryDone, mapStatusType("closed"))
	assert.Equal(t, tracker.CategoryUnknown, mapStatusType("unknown"))
}

func TestParseUnixMs(t *testing.T) {
	ts := parseUnixMs("1700000000000")
	assert.False(t, ts.IsZero())
	assert.Equal(t, int64(1700000000000), ts.UnixMilli())

	// Invalid input
	ts = parseUnixMs("not-a-number")
	assert.True(t, ts.IsZero())

	// Empty input
	ts = parseUnixMs("")
	assert.True(t, ts.IsZero())
}

func TestListSpaces_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/team/team1/space", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{
			"spaces": [
				{"id": "sp1", "name": "Engineering"},
				{"id": "sp2", "name": "Marketing"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "team1")
	spaces, err := client.ListSpaces(context.Background(), "team1")
	require.NoError(t, err)
	require.Len(t, spaces, 2)
	assert.Equal(t, "sp1", spaces[0].ID)
	assert.Equal(t, "Engineering", spaces[0].Name)
	assert.Equal(t, "sp2", spaces[1].ID)
	assert.Equal(t, "Marketing", spaces[1].Name)
}

func TestListFolders_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/space/sp1/folder", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{
			"folders": [
				{"id": "f1", "name": "Sprint Board"},
				{"id": "f2", "name": "Backlog"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	folders, err := client.ListFolders(context.Background(), "sp1")
	require.NoError(t, err)
	require.Len(t, folders, 2)
	assert.Equal(t, "f1", folders[0].ID)
	assert.Equal(t, "Sprint Board", folders[0].Name)
	assert.Equal(t, "f2", folders[1].ID)
	assert.Equal(t, "Backlog", folders[1].Name)
}

func TestListLists_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/folder/f1/list", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{
			"lists": [
				{"id": "901", "name": "Sprint 1"},
				{"id": "902", "name": "Sprint 2"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	lists, err := client.ListLists(context.Background(), "f1")
	require.NoError(t, err)
	require.Len(t, lists, 2)
	assert.Equal(t, "901", lists[0].ID)
	assert.Equal(t, "Sprint 1", lists[0].Name)
	assert.Equal(t, "902", lists[1].ID)
	assert.Equal(t, "Sprint 2", lists[1].Name)
}

func TestListFolderlessLists_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/space/sp1/list", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{
			"lists": [
				{"id": "903", "name": "Misc"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	lists, err := client.ListFolderlessLists(context.Background(), "sp1")
	require.NoError(t, err)
	require.Len(t, lists, 1)
	assert.Equal(t, "903", lists[0].ID)
	assert.Equal(t, "Misc", lists[0].Name)
}

func TestListWorkspaceMembers_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/team", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{
			"teams": [
				{
					"id": "team1",
					"name": "My Workspace",
					"members": [
						{"user": {"id": 100, "username": "alice", "email": "alice@example.com"}},
						{"user": {"id": 200, "username": "bob", "email": "bob@example.com"}}
					]
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "team1")
	members, err := client.ListWorkspaceMembers(context.Background(), "team1")
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, int64(100), members[0].ID)
	assert.Equal(t, "alice", members[0].Username)
	assert.Equal(t, "alice@example.com", members[0].Email)
	assert.Equal(t, int64(200), members[1].ID)
	assert.Equal(t, "bob", members[1].Username)
}

func TestListWorkspaceMembers_teamNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"teams": [{"id": "other", "name": "Other", "members": []}]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "team1")
	_, err := client.ListWorkspaceMembers(context.Background(), "team1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workspace not found")
}

func TestGetCustomFields_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/abc1", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{
			"id": "abc1",
			"name": "Test task",
			"description": "",
			"status": {"status": "open", "type": "open"},
			"assignees": [],
			"creator": {"id": 100, "username": "alice"},
			"date_created": "1700000000000",
			"date_updated": "1700100000000",
			"url": "https://app.clickup.com/t/abc1",
			"list": {"id": "901", "name": "Sprint 1"},
			"custom_fields": [
				{"id": "cf1", "name": "Story Points", "type": "number", "value": 5, "required": false},
				{"id": "cf2", "name": "Sprint", "type": "dropdown", "value": "Sprint 3", "required": true}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	fields, err := client.GetCustomFields(context.Background(), "abc1")
	require.NoError(t, err)
	require.Len(t, fields, 2)
	assert.Equal(t, "cf1", fields[0].ID)
	assert.Equal(t, "Story Points", fields[0].Name)
	assert.Equal(t, "number", fields[0].Type)
	assert.Equal(t, float64(5), fields[0].Value)
	assert.Equal(t, "cf2", fields[1].ID)
	assert.Equal(t, "Sprint", fields[1].Name)
	assert.Equal(t, true, fields[1].Required)
}

func TestSetCustomField_happy(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/abc1/field/cf1", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.SetCustomField(context.Background(), "abc1", "cf1", "8")
	require.NoError(t, err)
	assert.Equal(t, "8", gotBody["value"])
}

func TestCreateIssue_withParent(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/list/901/task", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = fmt.Fprint(w, `{
			"id": "new1",
			"name": "Child task",
			"description": "",
			"url": "https://app.clickup.com/t/new1",
			"parent": "parent1"
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	issue, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project:   "901",
		Title:     "Child task",
		ParentKey: "parent1",
	})
	require.NoError(t, err)
	assert.Equal(t, "new1", issue.Key)
	assert.Equal(t, "parent1", gotBody["parent"])
}

func TestToTrackerIssue_withTagsAndParent(t *testing.T) {
	client := New("http://localhost", "tok-test", "")
	task := cuTask{
		ID:          "abc1",
		Name:        "Tagged task",
		Description: "A task with tags",
		Status:      cuStatus{Status: "open", Type: "open"},
		Assignees:   []cuUser{},
		Creator:     cuUser{ID: 100, Username: "alice"},
		DateUpdated: "1700100000000",
		URL:         "https://app.clickup.com/t/abc1",
		List:        cuListRef{ID: "901", Name: "Sprint 1"},
		Parent:      "parent1",
		Tags:        []cuTag{{Name: "bug"}, {Name: "urgent"}},
	}

	issue := client.toTrackerIssue(context.Background(), task)
	assert.Equal(t, "parent1", issue.ParentKey)
	assert.Equal(t, []string{"bug", "urgent"}, issue.Labels)
	assert.Equal(t, "abc1", issue.Key)
}

func TestTeamID(t *testing.T) {
	client := New("http://localhost", "tok-test", "team123")
	assert.Equal(t, "team123", client.TeamID())

	client2 := New("http://localhost", "tok-test", "")
	assert.Equal(t, "", client2.TeamID())
}

func TestGetMarkdownDescription_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/abc1", r.URL.Path)
		assert.Equal(t, "include_markdown_description=true", r.URL.RawQuery)
		_, _ = fmt.Fprint(w, `{
			"id": "abc1",
			"name": "Test",
			"description": "plain text",
			"markdown_description": "# Title\n\nSome **bold** text",
			"status": {"status": "open", "type": "open"},
			"assignees": [],
			"creator": {"id": 100, "username": "alice"},
			"date_created": "1700000000000",
			"date_updated": "1700100000000",
			"url": "https://app.clickup.com/t/abc1",
			"list": {"id": "901", "name": "Sprint 1"}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	md, err := client.GetMarkdownDescription(context.Background(), "abc1")
	require.NoError(t, err)
	assert.Equal(t, "# Title\n\nSome **bold** text", md)
}

func TestSetMarkdownDescription_happy(t *testing.T) {
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/abc1", r.URL.Path)
		assert.Equal(t, http.MethodPut, r.Method)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = fmt.Fprint(w, `{"id": "abc1"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.SetMarkdownDescription(context.Background(), "abc1", "# Updated\n\nNew content")
	require.NoError(t, err)
	assert.Equal(t, "# Updated\n\nNew content", gotBody["markdown_description"])
}

// --- HTTP error tests ---

func TestGetIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.GetIssue(context.Background(), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestCreateIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Project: "901",
		Title:   "Test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestCreateIssue_requiresProject(t *testing.T) {
	client := New("http://localhost", "tok-test", "")
	_, err := client.CreateIssue(context.Background(), &tracker.Issue{
		Title: "Test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project")
}

func TestAddComment_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.AddComment(context.Background(), "abc123", "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestDeleteIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.DeleteIssue(context.Background(), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestTransitionIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.TransitionIssue(context.Background(), "abc123", "in progress")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAssignIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.AssignIssue(context.Background(), "abc123", "12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestAssignIssue_invalidUserID(t *testing.T) {
	client := New("http://localhost", "tok-test", "")
	err := client.AssignIssue(context.Background(), "abc123", "not-a-number")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestGetCurrentUser_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.GetCurrentUser(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestEditIssue_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	title := "Updated"
	client := New(srv.URL, "tok-test", "")
	_, err := client.EditIssue(context.Background(), "abc123", tracker.EditOptions{
		Title: &title,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListStatuses_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.ListStatuses(context.Background(), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListComments_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.ListComments(context.Background(), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListSpaces_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "team1")
	_, err := client.ListSpaces(context.Background(), "team1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListFolders_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.ListFolders(context.Background(), "sp1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListLists_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.ListLists(context.Background(), "f1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListFolderlessLists_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.ListFolderlessLists(context.Background(), "sp1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestListWorkspaceMembers_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "team1")
	_, err := client.ListWorkspaceMembers(context.Background(), "team1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetCustomFields_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.GetCustomFields(context.Background(), "abc1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestSetCustomField_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.SetCustomField(context.Background(), "abc1", "cf1", "8")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetMarkdownDescription_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	_, err := client.GetMarkdownDescription(context.Background(), "abc1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestSetMarkdownDescription_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "")
	err := client.SetMarkdownDescription(context.Background(), "abc1", "# Test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestGetMarkdownDescription_withCustomID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/PROJ-42", r.URL.Path)
		// Verify both include_markdown_description and custom ID params are present
		q := r.URL.Query()
		assert.Equal(t, "true", q.Get("include_markdown_description"))
		assert.Equal(t, "true", q.Get("custom_task_ids"))
		assert.Equal(t, "9876", q.Get("team_id"))

		_, _ = fmt.Fprint(w, `{
			"id": "abc456",
			"custom_id": "PROJ-42",
			"name": "Test",
			"description": "plain",
			"markdown_description": "# Custom ID markdown",
			"status": {"status": "open", "type": "open"},
			"assignees": [],
			"creator": {},
			"list": {"id": "901"}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "tok-test", "9876")
	md, err := client.GetMarkdownDescription(context.Background(), "PROJ-42")
	require.NoError(t, err)
	assert.Equal(t, "# Custom ID markdown", md)
}
