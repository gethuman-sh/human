package tracker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// mockDestructiveNotifier records calls for verification.
type mockDestructiveNotifier struct {
	mu      sync.Mutex
	entries []tracker.DestructiveEntry
	errFn   func() // optional: called to simulate error behavior
}

func (m *mockDestructiveNotifier) NotifyDestructive(_ context.Context, entry tracker.DestructiveEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	if m.errFn != nil {
		m.errFn()
	}
}

func (m *mockDestructiveNotifier) getEntries() []tracker.DestructiveEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]tracker.DestructiveEntry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

// readDestructiveEntries reads all JSON lines from the destructive log file.
func readDestructiveEntries(t *testing.T, path string) []tracker.DestructiveEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var entries []tracker.DestructiveEntry
	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var e tracker.DestructiveEntry
		require.NoError(t, json.Unmarshal(line, &e))
		entries = append(entries, e)
	}
	return entries
}

func newDestructive(t *testing.T, inner tracker.Provider, notifier tracker.DestructiveNotifier) (*tracker.DestructiveProvider, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "destructive.log")
	dp, err := tracker.NewDestructiveProvider(inner, "testtracker", "jira", logPath, notifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dp.Close() })
	return dp, logPath
}

func TestDestructiveProvider_DeleteIssue_Logged(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	err := dp.DeleteIssue(context.Background(), "KAN-5")
	require.NoError(t, err)

	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "DeleteIssue", entries[0].Operation)
	assert.Equal(t, "testtracker", entries[0].Tracker)
	assert.Equal(t, "jira", entries[0].Kind)
	assert.Equal(t, "KAN-5", entries[0].Key)
	assert.Empty(t, entries[0].Error)
	assert.NotEmpty(t, entries[0].Timestamp)
}

func TestDestructiveProvider_EditIssue_Logged(t *testing.T) {
	title := "New Title"
	inner := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: *opts.Title}, nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	issue, err := dp.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{Title: &title})
	require.NoError(t, err)
	assert.Equal(t, "KAN-1", issue.Key)

	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "EditIssue", entries[0].Operation)
	assert.Equal(t, "KAN-1", entries[0].Key)
	assert.Contains(t, entries[0].Detail, "title")
}

func TestDestructiveProvider_TransitionIssue_Logged(t *testing.T) {
	inner := &mockProvider{
		transitionIssueFn: func(_ context.Context, key string, targetStatus string) error {
			return nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	err := dp.TransitionIssue(context.Background(), "KAN-1", "Done")
	require.NoError(t, err)

	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "TransitionIssue", entries[0].Operation)
	assert.Equal(t, "KAN-1", entries[0].Key)
	assert.Contains(t, entries[0].Detail, "Done")
}

func TestDestructiveProvider_ReadMethodsNotLogged(t *testing.T) {
	inner := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		listCommentsFn: func(_ context.Context, issueKey string) ([]tracker.Comment, error) {
			return []tracker.Comment{{ID: "c-1"}}, nil
		},
		listStatusesFn: func(_ context.Context, key string) ([]tracker.Status, error) {
			return []tracker.Status{{Name: "Open"}}, nil
		},
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	_, _ = dp.GetIssue(context.Background(), "KAN-1")
	_, _ = dp.ListIssues(context.Background(), tracker.ListOptions{Project: "KAN"})
	_, _ = dp.ListComments(context.Background(), "KAN-1")
	_, _ = dp.ListStatuses(context.Background(), "KAN-1")
	_, _ = dp.GetCurrentUser(context.Background())

	entries := readDestructiveEntries(t, logPath)
	assert.Empty(t, entries, "read methods should not be logged to destructive log")
}

func TestDestructiveProvider_CreateIssueNotLogged(t *testing.T) {
	inner := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			return &tracker.Issue{Key: "KAN-99", Project: issue.Project}, nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	_, err := dp.CreateIssue(context.Background(), &tracker.Issue{Project: "KAN", Title: "New"})
	require.NoError(t, err)

	entries := readDestructiveEntries(t, logPath)
	assert.Empty(t, entries, "CreateIssue should not be logged to destructive log")
}

func TestDestructiveProvider_ErrorCaptured(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return fmt.Errorf("forbidden")
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	err := dp.DeleteIssue(context.Background(), "KAN-5")
	require.Error(t, err)

	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "forbidden", entries[0].Error)
}

func TestDestructiveProvider_NotifierCalled(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return nil
		},
	}
	notifier := &mockDestructiveNotifier{}
	dp, _ := newDestructive(t, inner, notifier)

	err := dp.DeleteIssue(context.Background(), "KAN-5")
	require.NoError(t, err)

	notified := notifier.getEntries()
	require.Len(t, notified, 1)
	assert.Equal(t, "DeleteIssue", notified[0].Operation)
	assert.Equal(t, "KAN-5", notified[0].Key)
}

func TestDestructiveProvider_NotifierNil(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return nil
		},
	}
	// No notifier -- should not panic
	dp, _ := newDestructive(t, inner, nil)

	err := dp.DeleteIssue(context.Background(), "KAN-5")
	require.NoError(t, err)
}

func TestDestructiveProvider_NotifierErrorIgnored(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, key string) error {
			return nil
		},
	}
	notifier := &mockDestructiveNotifier{
		errFn: func() {
			// Simulate a panic-free error scenario.
			// The notifier interface is fire-and-forget (no error return),
			// so the provider does not see errors.
		},
	}
	dp, logPath := newDestructive(t, inner, notifier)

	err := dp.DeleteIssue(context.Background(), "KAN-5")
	require.NoError(t, err)

	// Entry should still be logged even when notifier has issues.
	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
}

func TestDestructiveProvider_EditDetail_MultipleFields(t *testing.T) {
	title := "New Title"
	desc := "New Description"
	inner := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	_, err := dp.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{Title: &title, Description: &desc})
	require.NoError(t, err)

	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Detail, "title")
	assert.Contains(t, entries[0].Detail, "description")
}

// A no-op EditIssue (both fields nil) must log an empty Detail rather
// than a dangling "changed: " prefix. The audit log is the only record
// of what was edited; a stray prefix would misleadingly suggest a
// mutation happened and muddy compliance review.
func TestDestructiveProvider_EditDetail_EmptyOptions(t *testing.T) {
	inner := &mockProvider{
		editIssueFn: func(_ context.Context, key string, _ tracker.EditOptions) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
	}
	dp, logPath := newDestructive(t, inner, nil)

	_, err := dp.EditIssue(context.Background(), "KAN-1", tracker.EditOptions{})
	require.NoError(t, err)

	entries := readDestructiveEntries(t, logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].Detail,
		"empty EditOptions must produce an empty Detail, not a stray %q prefix", "changed: ")
}

func TestNewDestructiveProvider_InvalidPath(t *testing.T) {
	inner := &mockProvider{}
	_, err := tracker.NewDestructiveProvider(inner, "test", "jira", "/nonexistent/dir/destructive.log", nil)
	require.Error(t, err)
}
