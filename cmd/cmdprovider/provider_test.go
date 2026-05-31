package cmdprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/github"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/tracker"
)

// --- fake forge.Creator ---

type fakeForge struct {
	createFn func(ctx context.Context, pr *forge.PullRequest) (*forge.PullRequest, error)
}

func (f *fakeForge) CreatePullRequest(ctx context.Context, pr *forge.PullRequest) (*forge.PullRequest, error) {
	return f.createFn(ctx, pr)
}

// --- mock tracker.Provider ---

type mockProvider struct {
	listIssuesFn     func(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error)
	getIssueFn       func(ctx context.Context, key string) (*tracker.Issue, error)
	createIssueFn    func(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error)
	editIssueFn      func(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error)
	deleteIssueFn    func(ctx context.Context, key string) error
	addCommentFn     func(ctx context.Context, key, body string) (*tracker.Comment, error)
	listCommentsFn   func(ctx context.Context, key string) ([]tracker.Comment, error)
	transitionFn     func(ctx context.Context, key, status string) error
	assignFn         func(ctx context.Context, key, userID string) error
	getCurrentUserFn func(ctx context.Context) (string, error)
	listStatusesFn   func(ctx context.Context, key string) ([]tracker.Status, error)
}

func (m *mockProvider) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	return m.listIssuesFn(ctx, opts)
}

func (m *mockProvider) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	return m.getIssueFn(ctx, key)
}

func (m *mockProvider) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	return m.createIssueFn(ctx, issue)
}

func (m *mockProvider) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	return m.editIssueFn(ctx, key, opts)
}

func (m *mockProvider) DeleteIssue(ctx context.Context, key string) error {
	return m.deleteIssueFn(ctx, key)
}

func (m *mockProvider) AddComment(ctx context.Context, key, body string) (*tracker.Comment, error) {
	return m.addCommentFn(ctx, key, body)
}

func (m *mockProvider) ListComments(ctx context.Context, key string) ([]tracker.Comment, error) {
	return m.listCommentsFn(ctx, key)
}

func (m *mockProvider) TransitionIssue(ctx context.Context, key, status string) error {
	return m.transitionFn(ctx, key, status)
}

func (m *mockProvider) AssignIssue(ctx context.Context, key, userID string) error {
	return m.assignFn(ctx, key, userID)
}

func (m *mockProvider) GetCurrentUser(ctx context.Context) (string, error) {
	return m.getCurrentUserFn(ctx)
}

func (m *mockProvider) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	return m.listStatusesFn(ctx, key)
}

// --- RunListIssues tests ---

func TestRunListIssues_JSON(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "First issue", Status: "Open"},
		{Key: "KAN-2", Title: "Second issue", Status: "Done"},
	}
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			assert.Equal(t, "KAN", opts.Project)
			assert.Equal(t, 50, opts.MaxResults)
			assert.False(t, opts.IncludeAll)
			return issues, nil
		},
	}

	var buf bytes.Buffer
	err := RunListIssues(context.Background(), p, &buf, "KAN", false, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"key": "KAN-1"`)
	assert.Contains(t, buf.String(), `"key": "KAN-2"`)
}

func TestRunListIssues_Table(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "First issue", Status: "Open"},
	}
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return issues, nil
		},
	}

	var buf bytes.Buffer
	err := RunListIssues(context.Background(), p, &buf, "KAN", false, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KEY")
	assert.Contains(t, buf.String(), "STATUS")
	assert.Contains(t, buf.String(), "TITLE")
	assert.Contains(t, buf.String(), "KAN-1")
	assert.Contains(t, buf.String(), "Open")
}

func TestRunListIssues_IncludeAll(t *testing.T) {
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			assert.True(t, opts.IncludeAll)
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := RunListIssues(context.Background(), p, &buf, "KAN", true, false)
	require.NoError(t, err)
}

func TestRunListIssues_Error(t *testing.T) {
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return nil, errors.WithDetails("list failed")
		},
	}

	var buf bytes.Buffer
	err := RunListIssues(context.Background(), p, &buf, "KAN", false, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "list failed")
}

// --- RunGetIssue tests ---

func TestRunGetIssue_Success(t *testing.T) {
	p := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-42", key)
			return &tracker.Issue{
				Key:         "KAN-42",
				Title:       "Fix the bug",
				Status:      "In Progress",
				Priority:    "High",
				Assignee:    "alice",
				Reporter:    "bob",
				Description: "This is the description.",
			}, nil
		},
	}

	var buf bytes.Buffer
	err := RunGetIssue(context.Background(), p, &buf, "KAN-42")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "# KAN-42: Fix the bug")
	assert.Contains(t, out, "| Status   | In Progress |")
	assert.Contains(t, out, "| Priority | High |")
	assert.Contains(t, out, "| Assignee | alice |")
	assert.Contains(t, out, "| Reporter | bob |")
	assert.Contains(t, out, "## Description")
	assert.Contains(t, out, "This is the description.")
}

func TestRunGetIssue_EmptyFields(t *testing.T) {
	p := &mockProvider{
		getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
			return &tracker.Issue{
				Key:    "KAN-1",
				Title:  "Minimal",
				Status: "Open",
			}, nil
		},
	}

	var buf bytes.Buffer
	err := RunGetIssue(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "| Priority | None |")
	assert.Contains(t, out, "| Assignee | None |")
	assert.Contains(t, out, "| Reporter | None |")
	assert.NotContains(t, out, "## Description")
	// No parent row when the issue is not a subtask.
	assert.NotContains(t, out, "| Parent")
}

func TestRunGetIssue_WithParent(t *testing.T) {
	p := &mockProvider{
		getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
			return &tracker.Issue{
				Key:       "KAN-50",
				Title:     "Child task",
				Status:    "To Do",
				ParentKey: "KAN-1",
			}, nil
		},
	}

	var buf bytes.Buffer
	err := RunGetIssue(context.Background(), p, &buf, "KAN-50")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "| Parent   | KAN-1 |")
}

func TestRunGetIssue_Error(t *testing.T) {
	p := &mockProvider{
		getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
			return nil, errors.WithDetails("not found", "key", "KAN-99")
		},
	}

	var buf bytes.Buffer
	err := RunGetIssue(context.Background(), p, &buf, "KAN-99")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- RunCreateIssue tests ---

func TestRunCreateIssue_Success(t *testing.T) {
	p := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			assert.Equal(t, "KAN", issue.Project)
			assert.Equal(t, "Task", issue.Type)
			assert.Equal(t, "New feature", issue.Title)
			assert.Equal(t, "Details here", issue.Description)
			return &tracker.Issue{Key: "KAN-10", Title: "New feature"}, nil
		},
	}

	var buf bytes.Buffer
	err := RunCreateIssue(context.Background(), p, &buf, "KAN", "Task", "New feature", "Details here", "")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-10")
	assert.Contains(t, buf.String(), "New feature")
}

func TestRunCreateIssue_Error(t *testing.T) {
	p := &mockProvider{
		createIssueFn: func(_ context.Context, _ *tracker.Issue) (*tracker.Issue, error) {
			return nil, errors.WithDetails("create failed")
		},
	}

	var buf bytes.Buffer
	err := RunCreateIssue(context.Background(), p, &buf, "KAN", "Task", "Title", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create failed")
}

func TestRunCreatePullRequest_Success(t *testing.T) {
	f := &fakeForge{
		createFn: func(_ context.Context, pr *forge.PullRequest) (*forge.PullRequest, error) {
			assert.Equal(t, "octocat/hello-world", pr.Repo)
			assert.Equal(t, "main", pr.Base)
			assert.Equal(t, "fix-login", pr.Head)
			assert.Equal(t, "Fix login", pr.Title)
			return &forge.PullRequest{
				Number: 7,
				Title:  "Fix login",
				URL:    "https://github.com/octocat/hello-world/pull/7",
			}, nil
		},
	}

	var buf bytes.Buffer
	err := RunCreatePullRequest(context.Background(), f, &buf, "octocat/hello-world", "main", "fix-login", "Fix login", "body")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "https://github.com/octocat/hello-world/pull/7")
	assert.Contains(t, buf.String(), "Fix login")
}

func TestRunCreatePullRequest_Error(t *testing.T) {
	f := &fakeForge{
		createFn: func(_ context.Context, _ *forge.PullRequest) (*forge.PullRequest, error) {
			return nil, errors.WithDetails("pr failed")
		},
	}

	var buf bytes.Buffer
	err := RunCreatePullRequest(context.Background(), f, &buf, "octocat/hello-world", "main", "fix-login", "Fix login", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pr failed")
}

// --- RunEditIssue tests ---

func TestRunEditIssue_Success(t *testing.T) {
	newTitle := "Updated title"
	p := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			assert.NotNil(t, opts.Title)
			assert.Equal(t, "Updated title", *opts.Title)
			return &tracker.Issue{Key: "KAN-1", Title: "Updated title"}, nil
		},
	}

	var buf bytes.Buffer
	err := RunEditIssue(context.Background(), p, &buf, "KAN-1", tracker.EditOptions{Title: &newTitle})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
	assert.Contains(t, buf.String(), "Updated title")
}

func TestRunEditIssue_NilIssueReturned(t *testing.T) {
	p := &mockProvider{
		editIssueFn: func(_ context.Context, _ string, _ tracker.EditOptions) (*tracker.Issue, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := RunEditIssue(context.Background(), p, &buf, "KAN-1", tracker.EditOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "edit returned no issue")
}

func TestRunEditIssue_Error(t *testing.T) {
	p := &mockProvider{
		editIssueFn: func(_ context.Context, _ string, _ tracker.EditOptions) (*tracker.Issue, error) {
			return nil, errors.WithDetails("edit failed")
		},
	}

	var buf bytes.Buffer
	err := RunEditIssue(context.Background(), p, &buf, "KAN-1", tracker.EditOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "edit failed")
}

// --- RunDeleteIssue tests ---

func TestRunDeleteIssue_Success(t *testing.T) {
	deleted := false
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			assert.Equal(t, "KAN-1", key)
			deleted = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader(""), &buf, "KAN-1", true)
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Contains(t, buf.String(), "Deleted KAN-1")
}

func TestRunDeleteIssue_DeleteError(t *testing.T) {
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			return errors.WithDetails("delete failed")
		},
	}

	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader(""), &buf, "KAN-1", true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "delete failed")
}

func TestRunDeleteIssue_EmptyInputCancels(t *testing.T) {
	deleted := false
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			deleted = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader("\n"), &buf, "KAN-1", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete cancelled")
	assert.False(t, deleted, "DeleteIssue must not be called on bare Enter")
}

func TestRunDeleteIssue_NInputCancels(t *testing.T) {
	deleted := false
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			deleted = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader("N\n"), &buf, "KAN-1", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete cancelled")
	assert.False(t, deleted)
}

func TestRunDeleteIssue_YInputDeletes(t *testing.T) {
	deleted := false
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			assert.Equal(t, "KAN-1", key)
			deleted = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader("y\n"), &buf, "KAN-1", false)
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Contains(t, buf.String(), "Deleted KAN-1")
}

func TestRunDeleteIssue_WhitespaceOnlyCancels(t *testing.T) {
	deleted := false
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			deleted = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader("   \n"), &buf, "KAN-1", false)
	require.Error(t, err)
	assert.False(t, deleted, "DeleteIssue must not be called on whitespace-only input")
}

func TestRunDeleteIssue_YesFlagSkipsPrompt(t *testing.T) {
	deleted := false
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			deleted = true
			return nil
		},
	}

	// Empty stdin, but yes=true should bypass the prompt entirely.
	var buf bytes.Buffer
	err := RunDeleteIssue(context.Background(), p, strings.NewReader(""), &buf, "KAN-1", true)
	require.NoError(t, err)
	assert.True(t, deleted)
}

// --- RunStartIssue tests ---

func TestRunStartIssue_Success(t *testing.T) {
	p := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-123", nil
		},
		transitionFn: func(_ context.Context, key, status string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "In Progress", status)
			return nil
		},
		assignFn: func(_ context.Context, key, userID string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "user-123", userID)
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunStartIssue(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Started KAN-1")
}

func TestRunStartIssue_GetCurrentUserError(t *testing.T) {
	p := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "", errors.WithDetails("auth failed")
		},
	}

	var buf bytes.Buffer
	err := RunStartIssue(context.Background(), p, &buf, "KAN-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting current user")
}

func TestRunStartIssue_TransitionFails_AssignSucceeds(t *testing.T) {
	p := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
		transitionFn: func(_ context.Context, _, _ string) error {
			return errors.WithDetails("transition error")
		},
		assignFn: func(_ context.Context, _, _ string) error {
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunStartIssue(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Assigned KAN-1 to user-1")
	assert.Contains(t, buf.String(), "transition failed")
}

func TestRunStartIssue_AssignFails_TransitionSucceeds(t *testing.T) {
	p := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
		transitionFn: func(_ context.Context, _, _ string) error {
			return nil
		},
		assignFn: func(_ context.Context, _, _ string) error {
			return errors.WithDetails("assign error")
		},
	}

	var buf bytes.Buffer
	err := RunStartIssue(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Transitioned KAN-1 to In Progress")
	assert.Contains(t, buf.String(), "assign failed")
}

func TestRunStartIssue_BothFail(t *testing.T) {
	p := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
		transitionFn: func(_ context.Context, _, _ string) error {
			return errors.WithDetails("transition error")
		},
		assignFn: func(_ context.Context, _, _ string) error {
			return errors.WithDetails("assign error")
		},
	}

	var buf bytes.Buffer
	err := RunStartIssue(context.Background(), p, &buf, "KAN-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start issue")
}

// --- RunAddComment tests ---

func TestRunAddComment_Success(t *testing.T) {
	p := &mockProvider{
		addCommentFn: func(_ context.Context, key, body string) (*tracker.Comment, error) {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "This is a comment", body)
			return &tracker.Comment{ID: "c-1", Body: "This is a comment"}, nil
		},
	}

	var buf bytes.Buffer
	err := RunAddComment(context.Background(), p, &buf, "KAN-1", "This is a comment")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "c-1")
	assert.Contains(t, buf.String(), "This is a comment")
}

func TestRunAddComment_Error(t *testing.T) {
	p := &mockProvider{
		addCommentFn: func(_ context.Context, _, _ string) (*tracker.Comment, error) {
			return nil, errors.WithDetails("comment failed")
		},
	}

	var buf bytes.Buffer
	err := RunAddComment(context.Background(), p, &buf, "KAN-1", "body")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "comment failed")
}

// --- RunListComments tests ---

func TestRunListComments_Success(t *testing.T) {
	comments := []tracker.Comment{
		{ID: "c-1", Author: "alice", Body: "First comment"},
		{ID: "c-2", Author: "bob", Body: "Second comment"},
	}
	p := &mockProvider{
		listCommentsFn: func(_ context.Context, key string) ([]tracker.Comment, error) {
			assert.Equal(t, "KAN-1", key)
			return comments, nil
		},
	}

	var buf bytes.Buffer
	err := RunListComments(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)

	// Output should be valid JSON
	var parsed []tracker.Comment
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err)
	assert.Len(t, parsed, 2)
	assert.Equal(t, "c-1", parsed[0].ID)
}

func TestRunListComments_Error(t *testing.T) {
	p := &mockProvider{
		listCommentsFn: func(_ context.Context, _ string) ([]tracker.Comment, error) {
			return nil, errors.WithDetails("comments failed")
		},
	}

	var buf bytes.Buffer
	err := RunListComments(context.Background(), p, &buf, "KAN-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "comments failed")
}

// --- RunListStatuses tests ---

func TestRunListStatuses_JSON(t *testing.T) {
	statuses := []tracker.Status{
		{Name: "Open", Category: "unstarted"},
		{Name: "In Progress", Category: "started"},
		{Name: "Done", Category: "done"},
	}
	p := &mockProvider{
		listStatusesFn: func(_ context.Context, key string) ([]tracker.Status, error) {
			assert.Equal(t, "KAN-1", key)
			return statuses, nil
		},
	}

	var buf bytes.Buffer
	err := RunListStatuses(context.Background(), p, &buf, "KAN-1", false)
	require.NoError(t, err)

	var parsed []tracker.Status
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err)
	assert.Len(t, parsed, 3)
	assert.Equal(t, "Open", parsed[0].Name)
}

func TestRunListStatuses_Table(t *testing.T) {
	statuses := []tracker.Status{
		{Name: "Open", Category: "unstarted"},
		{Name: "In Progress", Category: "started"},
	}
	p := &mockProvider{
		listStatusesFn: func(_ context.Context, _ string) ([]tracker.Status, error) {
			return statuses, nil
		},
	}

	var buf bytes.Buffer
	err := RunListStatuses(context.Background(), p, &buf, "KAN-1", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "TYPE")
	assert.Contains(t, buf.String(), "Open")
	assert.Contains(t, buf.String(), "In Progress")
}

func TestRunListStatuses_Error(t *testing.T) {
	p := &mockProvider{
		listStatusesFn: func(_ context.Context, _ string) ([]tracker.Status, error) {
			return nil, errors.WithDetails("statuses failed")
		},
	}

	var buf bytes.Buffer
	err := RunListStatuses(context.Background(), p, &buf, "KAN-1", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "statuses failed")
}

// --- RunSetStatus tests ---

func TestRunSetStatus_Success(t *testing.T) {
	p := &mockProvider{
		transitionFn: func(_ context.Context, key, status string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "Done", status)
			return nil
		},
	}

	var buf bytes.Buffer
	err := RunSetStatus(context.Background(), p, &buf, "KAN-1", "Done")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Transitioned KAN-1 to Done")
}

func TestRunSetStatus_Error(t *testing.T) {
	p := &mockProvider{
		transitionFn: func(_ context.Context, _, _ string) error {
			return errors.WithDetails("transition failed")
		},
	}

	var buf bytes.Buffer
	err := RunSetStatus(context.Background(), p, &buf, "KAN-1", "InvalidStatus")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "transition failed")
	// Should print hint before returning error
	assert.Contains(t, buf.String(), "Hint:")
	assert.Contains(t, buf.String(), "statuses")
}

// --- PrintStatusesTable tests ---

func TestPrintStatusesTable_Success(t *testing.T) {
	statuses := []tracker.Status{
		{Name: "Open", Category: "unstarted"},
		{Name: "Closed", Category: ""},
	}

	var buf bytes.Buffer
	err := PrintStatusesTable(&buf, statuses)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "TYPE")
	assert.Contains(t, buf.String(), "Open")
	assert.Contains(t, buf.String(), "unstarted")
	assert.Contains(t, buf.String(), "Closed")
	assert.Contains(t, buf.String(), "-") // empty type replaced with "-"
}

func TestPrintStatusesTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := PrintStatusesTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "TYPE")
}

// --- printIssuesTable tests ---

func TestPrintIssuesTable_Success(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Status: "Open", Title: "First"},
		{Key: "KAN-2", Status: "Done", Title: "Second"},
	}

	var buf bytes.Buffer
	err := printIssuesTable(&buf, issues)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KEY")
	assert.Contains(t, buf.String(), "STATUS")
	assert.Contains(t, buf.String(), "TITLE")
	assert.Contains(t, buf.String(), "KAN-1")
	assert.Contains(t, buf.String(), "KAN-2")
}

// --- printIssuesJSON tests ---

func TestPrintIssuesJSON_Success(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "Issue one"},
	}

	var buf bytes.Buffer
	err := printIssuesJSON(&buf, issues)
	require.NoError(t, err)

	var parsed []tracker.Issue
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err)
	assert.Len(t, parsed, 1)
	assert.Equal(t, "KAN-1", parsed[0].Key)
}

// --- BuildProviderCommands tests ---

func TestBuildProviderCommands_ReturnsExpectedCommands(t *testing.T) {
	cmds := BuildProviderCommands("jira", cmdutil.Deps{})
	require.Len(t, cmds, 2)

	issuesCmd := cmds[0]
	assert.Equal(t, "issues", issuesCmd.Use)

	issueCmd := cmds[1]
	assert.Equal(t, "issue", issueCmd.Use)

	// Check subcommands of "issue"
	subNames := make(map[string]bool)
	for _, sub := range issueCmd.Commands() {
		subNames[sub.Name()] = true
	}
	assert.True(t, subNames["get"], "expected 'get' subcommand")
	assert.True(t, subNames["create"], "expected 'create' subcommand")
	assert.True(t, subNames["edit"], "expected 'edit' subcommand")
	assert.True(t, subNames["delete"], "expected 'delete' subcommand")
	assert.True(t, subNames["comment"], "expected 'comment' subcommand")
	assert.True(t, subNames["start"], "expected 'start' subcommand")
	assert.True(t, subNames["statuses"], "expected 'statuses' subcommand")
	assert.True(t, subNames["status"], "expected 'status' subcommand")
}

func TestBuildProviderCommands_ForgeKindHasPRCommand(t *testing.T) {
	cmds := BuildProviderCommands("github", cmdutil.Deps{})

	var prCmd *cobra.Command
	for _, c := range cmds {
		if c.Name() == "pr" {
			prCmd = c
		}
	}
	require.NotNil(t, prCmd, "github should expose a 'pr' command")

	subNames := make(map[string]bool)
	for _, sub := range prCmd.Commands() {
		subNames[sub.Name()] = true
	}
	assert.True(t, subNames["create"], "expected 'create' subcommand under pr")
}

func TestBuildProviderCommands_NonForgeKindHasNoPRCommand(t *testing.T) {
	cmds := BuildProviderCommands("jira", cmdutil.Deps{})
	for _, c := range cmds {
		assert.NotEqual(t, "pr", c.Name(), "non-forge kind must not expose a 'pr' command")
	}
}

func TestPRCreate_DefaultsRepoFromOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/gethuman-sh/human/pulls", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":9,"title":"Fix","html_url":"https://github.com/gethuman-sh/human/pull/9"}`))
	}))
	defer srv.Close()

	prev := gitrepo.OriginURL
	gitrepo.OriginURL = func(_ context.Context, _ string) (string, error) {
		return "https://github.com/gethuman-sh/human.git", nil
	}
	defer func() { gitrepo.OriginURL = prev }()

	deps := cmdutil.Deps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return []tracker.Instance{{
				Name: "gh", Kind: "github", URL: srv.URL, Provider: github.New(srv.URL, "t"),
			}}, nil
		},
		InstanceFromFlags: func(_ *cobra.Command) *tracker.Instance { return nil },
		AuditLogPath:      func() string { return "" },
	}

	root := &cobra.Command{Use: "human"}
	for _, c := range BuildProviderCommands("github", deps) {
		root.AddCommand(c)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	// --repo omitted on purpose: it must be filled from the git origin remote.
	root.SetArgs([]string{"pr", "create", "--head", "fix-login", "--title", "Fix"})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "https://github.com/gethuman-sh/human/pull/9")
}

func TestBuildProviderCommands_IssuesHasListSubcommand(t *testing.T) {
	cmds := BuildProviderCommands("github", cmdutil.Deps{})
	issuesCmd := cmds[0]

	subNames := make(map[string]bool)
	for _, sub := range issuesCmd.Commands() {
		subNames[sub.Name()] = true
	}
	assert.True(t, subNames["list"], "expected 'list' subcommand")
}

// --- Command integration tests (exercise RunE closures via cobra) ---

// newTestRoot creates a root command with mock deps that returns the given mock provider.
func newTestRoot(mp *mockProvider) (*cobra.Command, cmdutil.Deps) {
	deps := cmdutil.Deps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return []tracker.Instance{
				{Name: "test", Kind: "jira", Provider: mp},
			}, nil
		},
		InstanceFromFlags: func(_ *cobra.Command) *tracker.Instance { return nil },
		AuditLogPath:      func() string { return "" },
	}

	root := &cobra.Command{Use: "human"}
	root.PersistentFlags().String("tracker", "", "")
	root.PersistentFlags().Bool("safe", false, "")

	jiraCmd := &cobra.Command{Use: "jira"}
	cmds := BuildProviderCommands("jira", deps)
	for _, c := range cmds {
		jiraCmd.AddCommand(c)
	}
	root.AddCommand(jiraCmd)

	return root, deps
}

func TestCmd_IssuesList_Success(t *testing.T) {
	mp := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			assert.Equal(t, "KAN", opts.Project)
			return []tracker.Issue{{Key: "KAN-1", Title: "Test"}}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issues", "list", "--project", "KAN"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
}

func TestCmd_IssuesList_Error(t *testing.T) {
	mp := &mockProvider{
		listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return nil, errors.WithDetails("list error")
		},
	}
	root, _ := newTestRoot(mp)
	root.SetArgs([]string{"jira", "issues", "list", "--project", "KAN"})
	root.SilenceErrors = true
	root.SilenceUsage = true
	err := root.Execute()
	assert.Error(t, err)
}

func TestCmd_IssueGet_Success(t *testing.T) {
	mp := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			return &tracker.Issue{Key: "KAN-1", Title: "Test", Status: "Open"}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "get", "KAN-1"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
}

func TestCmd_IssueCreate_Success(t *testing.T) {
	mp := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			assert.Equal(t, "KAN", issue.Project)
			assert.Equal(t, "New task", issue.Title)
			return &tracker.Issue{Key: "KAN-10", Title: "New task"}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "create", "--project", "KAN", "New task"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-10")
}

func TestCmd_IssueEdit_Success(t *testing.T) {
	mp := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			require.NotNil(t, opts.Title)
			assert.Equal(t, "New title", *opts.Title)
			return &tracker.Issue{Key: "KAN-1", Title: "New title"}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "edit", "KAN-1", "--title", "New title"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
}

func TestCmd_IssueEdit_NoFlags(t *testing.T) {
	mp := &mockProvider{}
	root, _ := newTestRoot(mp)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"jira", "issue", "edit", "KAN-1"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one of --title or --description is required")
}

func TestCmd_IssueDelete_Success(t *testing.T) {
	deleted := false
	mp := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			assert.Equal(t, "KAN-1", key)
			deleted = true
			return nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "delete", "KAN-1", "--yes"})
	err := root.Execute()
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Contains(t, buf.String(), "Deleted KAN-1")
}

func TestCmd_IssueCommentAdd_Success(t *testing.T) {
	mp := &mockProvider{
		addCommentFn: func(_ context.Context, key, body string) (*tracker.Comment, error) {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "Hello", body)
			return &tracker.Comment{ID: "c-1", Body: "Hello"}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "comment", "add", "KAN-1", "Hello"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "c-1")
}

func TestCmd_IssueCommentList_Success(t *testing.T) {
	mp := &mockProvider{
		listCommentsFn: func(_ context.Context, key string) ([]tracker.Comment, error) {
			assert.Equal(t, "KAN-1", key)
			return []tracker.Comment{{ID: "c-1", Body: "A comment"}}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "comment", "list", "KAN-1"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "c-1")
}

func TestCmd_IssueStart_Success(t *testing.T) {
	mp := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
		transitionFn: func(_ context.Context, _, _ string) error { return nil },
		assignFn:     func(_ context.Context, _, _ string) error { return nil },
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "start", "KAN-1"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Started KAN-1")
}

func TestCmd_IssueStatuses_Success(t *testing.T) {
	mp := &mockProvider{
		listStatusesFn: func(_ context.Context, key string) ([]tracker.Status, error) {
			return []tracker.Status{{Name: "Open", Category: "unstarted"}}, nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "statuses", "KAN-1"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Open")
}

func TestCmd_IssueStatusSet_Success(t *testing.T) {
	mp := &mockProvider{
		transitionFn: func(_ context.Context, key, status string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "Done", status)
			return nil
		},
	}
	root, _ := newTestRoot(mp)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"jira", "issue", "status", "KAN-1", "Done"})
	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Transitioned KAN-1 to Done")
}

func TestCmd_LoadInstancesError(t *testing.T) {
	deps := cmdutil.Deps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return nil, errors.WithDetails("config error")
		},
		InstanceFromFlags: func(_ *cobra.Command) *tracker.Instance { return nil },
		AuditLogPath:      func() string { return "" },
	}

	root := &cobra.Command{Use: "human"}
	root.PersistentFlags().String("tracker", "", "")
	root.PersistentFlags().Bool("safe", false, "")
	jiraCmd := &cobra.Command{Use: "jira"}
	cmds := BuildProviderCommands("jira", deps)
	for _, c := range cmds {
		jiraCmd.AddCommand(c)
	}
	root.AddCommand(jiraCmd)

	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"jira", "issue", "get", "KAN-1"})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config error")
}
