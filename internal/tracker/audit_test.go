package tracker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// --- mock provider for audit tests ---

type mockProvider struct {
	listIssuesFn      func(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error)
	getIssueFn        func(ctx context.Context, key string) (*tracker.Issue, error)
	createIssueFn     func(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error)
	deleteIssueFn     func(ctx context.Context, key string) error
	listCommentsFn    func(ctx context.Context, issueKey string) ([]tracker.Comment, error)
	addCommentFn      func(ctx context.Context, issueKey string, body string) (*tracker.Comment, error)
	transitionIssueFn func(ctx context.Context, key string, targetStatus string) error
	assignIssueFn     func(ctx context.Context, key string, userID string) error
	getCurrentUserFn  func(ctx context.Context) (string, error)
	editIssueFn       func(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error)
	listStatusesFn    func(ctx context.Context, key string) ([]tracker.Status, error)
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

func (m *mockProvider) DeleteIssue(ctx context.Context, key string) error {
	return m.deleteIssueFn(ctx, key)
}

func (m *mockProvider) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	return m.listCommentsFn(ctx, issueKey)
}

func (m *mockProvider) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	return m.addCommentFn(ctx, issueKey, body)
}

func (m *mockProvider) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	if m.transitionIssueFn != nil {
		return m.transitionIssueFn(ctx, key, targetStatus)
	}
	return nil
}

func (m *mockProvider) AssignIssue(ctx context.Context, key string, userID string) error {
	if m.assignIssueFn != nil {
		return m.assignIssueFn(ctx, key, userID)
	}
	return nil
}

func (m *mockProvider) GetCurrentUser(ctx context.Context) (string, error) {
	if m.getCurrentUserFn != nil {
		return m.getCurrentUserFn(ctx)
	}
	return "", nil
}

func (m *mockProvider) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	if m.editIssueFn != nil {
		return m.editIssueFn(ctx, key, opts)
	}
	return nil, nil
}

func (m *mockProvider) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	if m.listStatusesFn != nil {
		return m.listStatusesFn(ctx, key)
	}
	return nil, nil
}

// readEntries reads all JSON lines from the audit log file.
func readEntries(t *testing.T, path string) []tracker.AuditEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var entries []tracker.AuditEntry
	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var e tracker.AuditEntry
		require.NoError(t, json.Unmarshal(line, &e))
		entries = append(entries, e)
	}
	return entries
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func newAudit(t *testing.T, inner tracker.Provider) (*tracker.AuditProvider, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "audit.log")
	ap, err := tracker.NewAuditProvider(inner, "testtracker", "jira", logPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ap.Close() })
	return ap, logPath
}

func TestAuditProvider_ListIssues(t *testing.T) {
	inner := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	issues, err := ap.ListIssues(context.Background(), tracker.ListOptions{Project: "KAN"})
	require.NoError(t, err)
	require.Len(t, issues, 1)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "ListIssues", entries[0].Operation)
	assert.Equal(t, "testtracker", entries[0].Tracker)
	assert.Equal(t, "jira", entries[0].Kind)
	assert.Equal(t, "KAN", entries[0].Key)
	assert.Empty(t, entries[0].Error)
	assert.GreaterOrEqual(t, entries[0].DurationMs, int64(0))
	assert.NotEmpty(t, entries[0].Timestamp)
}

func TestAuditProvider_GetIssue(t *testing.T) {
	inner := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	issue, err := ap.GetIssue(context.Background(), "KAN-42")
	require.NoError(t, err)
	assert.Equal(t, "KAN-42", issue.Key)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "GetIssue", entries[0].Operation)
	assert.Equal(t, "KAN-42", entries[0].Key)
}

func TestAuditProvider_CreateIssue(t *testing.T) {
	inner := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			return &tracker.Issue{Key: "KAN-99", Project: issue.Project}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	created, err := ap.CreateIssue(context.Background(), &tracker.Issue{Project: "KAN", Title: "New"})
	require.NoError(t, err)
	assert.Equal(t, "KAN-99", created.Key)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "CreateIssue", entries[0].Operation)
	assert.Equal(t, "KAN", entries[0].Key)
}

func TestAuditProvider_DeleteIssue(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return nil
		},
	}
	ap, logPath := newAudit(t, inner)

	err := ap.DeleteIssue(context.Background(), "KAN-5")
	require.NoError(t, err)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "DeleteIssue", entries[0].Operation)
	assert.Equal(t, "KAN-5", entries[0].Key)
}

func TestAuditProvider_ListComments(t *testing.T) {
	inner := &mockProvider{
		listCommentsFn: func(_ context.Context, issueKey string) ([]tracker.Comment, error) {
			return []tracker.Comment{{ID: "c-1"}}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	comments, err := ap.ListComments(context.Background(), "KAN-10")
	require.NoError(t, err)
	require.Len(t, comments, 1)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "ListComments", entries[0].Operation)
	assert.Equal(t, "KAN-10", entries[0].Key)
}

func TestAuditProvider_AddComment(t *testing.T) {
	inner := &mockProvider{
		addCommentFn: func(_ context.Context, issueKey, body string) (*tracker.Comment, error) {
			return &tracker.Comment{ID: "c-2", Body: body}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	comment, err := ap.AddComment(context.Background(), "KAN-10", "hello")
	require.NoError(t, err)
	assert.Equal(t, "c-2", comment.ID)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "AddComment", entries[0].Operation)
	assert.Equal(t, "KAN-10", entries[0].Key)
}

func TestAuditProvider_ErrorCaptured(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return fmt.Errorf("forbidden")
		},
	}
	ap, logPath := newAudit(t, inner)

	err := ap.DeleteIssue(context.Background(), "KAN-5")
	require.Error(t, err)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "forbidden", entries[0].Error)
}

func TestAuditProvider_AppendsToExistingFile(t *testing.T) {
	inner := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	_, _ = ap.GetIssue(context.Background(), "KAN-1")
	_, _ = ap.GetIssue(context.Background(), "KAN-2")
	_, _ = ap.GetIssue(context.Background(), "KAN-3")

	entries := readEntries(t, logPath)
	require.Len(t, entries, 3)
	assert.Equal(t, "KAN-1", entries[0].Key)
	assert.Equal(t, "KAN-2", entries[1].Key)
	assert.Equal(t, "KAN-3", entries[2].Key)
}

func TestAuditProvider_DurationRecorded(t *testing.T) {
	inner := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	_, _ = ap.GetIssue(context.Background(), "KAN-1")

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	// Duration should be non-negative (may be 0ms for fast mock)
	assert.GreaterOrEqual(t, entries[0].DurationMs, int64(0))
}

func TestAuditProvider_TransitionIssue(t *testing.T) {
	inner := &mockProvider{
		transitionIssueFn: func(_ context.Context, key string, targetStatus string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "In Progress", targetStatus)
			return nil
		},
	}
	ap, logPath := newAudit(t, inner)

	err := ap.TransitionIssue(context.Background(), "KAN-1", "In Progress")
	require.NoError(t, err)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "TransitionIssue", entries[0].Operation)
	assert.Equal(t, "KAN-1", entries[0].Key)
}

func TestAuditProvider_AssignIssue(t *testing.T) {
	inner := &mockProvider{
		assignIssueFn: func(_ context.Context, key string, userID string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "user-123", userID)
			return nil
		},
	}
	ap, logPath := newAudit(t, inner)

	err := ap.AssignIssue(context.Background(), "KAN-1", "user-123")
	require.NoError(t, err)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "AssignIssue", entries[0].Operation)
	assert.Equal(t, "KAN-1", entries[0].Key)
}

func TestAuditProvider_GetCurrentUser(t *testing.T) {
	inner := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-123", nil
		},
	}
	ap, logPath := newAudit(t, inner)

	userID, err := ap.GetCurrentUser(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "user-123", userID)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "GetCurrentUser", entries[0].Operation)
	assert.Equal(t, "", entries[0].Key)
}

func TestAuditProvider_EditIssue(t *testing.T) {
	title := "New Title"
	inner := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, &title, opts.Title)
			return &tracker.Issue{Key: key, Title: title}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	issue, err := ap.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{Title: &title})
	require.NoError(t, err)
	assert.Equal(t, "KAN-1", issue.Key)
	assert.Equal(t, "New Title", issue.Title)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "EditIssue", entries[0].Operation)
	assert.Equal(t, "KAN-1", entries[0].Key)
}

func TestAuditProvider_ListStatuses(t *testing.T) {
	inner := &mockProvider{
		listStatusesFn: func(_ context.Context, key string) ([]tracker.Status, error) {
			assert.Equal(t, "KAN-1", key)
			return []tracker.Status{
				{Name: "To Do", Category: "unstarted"},
				{Name: "Done", Category: "done"},
			}, nil
		},
	}
	ap, logPath := newAudit(t, inner)

	statuses, err := ap.ListStatuses(context.Background(), "KAN-1")
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, "To Do", statuses[0].Name)
	assert.Equal(t, "Done", statuses[1].Name)

	entries := readEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "ListStatuses", entries[0].Operation)
	assert.Equal(t, "KAN-1", entries[0].Key)
}

func TestNewAuditProvider_InvalidPath(t *testing.T) {
	inner := &mockProvider{}
	_, err := tracker.NewAuditProvider(inner, "test", "jira", "/nonexistent/dir/audit.log")
	require.Error(t, err)
}
