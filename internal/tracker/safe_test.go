package tracker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestSafeProvider_ReadMethodsPassThrough(t *testing.T) {
	inner := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
		listCommentsFn: func(_ context.Context, issueKey string) ([]tracker.Comment, error) {
			return []tracker.Comment{{ID: "c-1"}}, nil
		},
		listStatusesFn: func(_ context.Context, key string) ([]tracker.Status, error) {
			return []tracker.Status{{Name: "To Do"}}, nil
		},
	}

	sp := tracker.NewSafeProvider(inner, "test-instance")
	ctx := context.Background()

	issues, err := sp.ListIssues(ctx, tracker.ListOptions{Project: "KAN"})
	require.NoError(t, err)
	assert.Len(t, issues, 1)

	issue, err := sp.GetIssue(ctx, "KAN-1")
	require.NoError(t, err)
	assert.Equal(t, "KAN-1", issue.Key)

	comments, err := sp.ListComments(ctx, "KAN-1")
	require.NoError(t, err)
	assert.Len(t, comments, 1)

	statuses, err := sp.ListStatuses(ctx, "KAN-1")
	require.NoError(t, err)
	assert.Len(t, statuses, 1)
}

func TestSafeProvider_WriteMethodsPassThrough(t *testing.T) {
	inner := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			return &tracker.Issue{Key: "KAN-99", Project: issue.Project}, nil
		},
		addCommentFn: func(_ context.Context, issueKey, body string) (*tracker.Comment, error) {
			return &tracker.Comment{ID: "c-2", Body: body}, nil
		},
		transitionIssueFn: func(_ context.Context, _ string, _ string) error {
			return nil
		},
		assignIssueFn: func(_ context.Context, _ string, _ string) error {
			return nil
		},
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
		editIssueFn: func(_ context.Context, key string, _ tracker.EditOptions) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
	}

	sp := tracker.NewSafeProvider(inner, "test-instance")
	ctx := context.Background()

	created, err := sp.CreateIssue(ctx, &tracker.Issue{Project: "KAN", Title: "New"})
	require.NoError(t, err)
	assert.Equal(t, "KAN-99", created.Key)

	comment, err := sp.AddComment(ctx, "KAN-1", "hello")
	require.NoError(t, err)
	assert.Equal(t, "c-2", comment.ID)

	err = sp.TransitionIssue(ctx, "KAN-1", "In Progress")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked by safe mode")

	err = sp.AssignIssue(ctx, "KAN-1", "user-1")
	require.NoError(t, err)

	userID, err := sp.GetCurrentUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "user-1", userID)

	title := "Updated"
	_, err = sp.EditIssue(ctx, "KAN-1", tracker.EditOptions{Title: &title})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked by safe mode")
}

func TestSafeProvider_DeleteIssueBlocked(t *testing.T) {
	deleteCalled := false
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			deleteCalled = true
			return nil
		},
	}

	sp := tracker.NewSafeProvider(inner, "prod-jira")

	err := sp.DeleteIssue(context.Background(), "KAN-5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation blocked by safe mode")
	assert.Contains(t, err.Error(), "prod-jira")
	assert.False(t, deleteCalled, "inner DeleteIssue should not be called")
}

func TestSafeProvider_DeleteIssueErrorIncludesInstanceName(t *testing.T) {
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			return nil
		},
	}

	sp := tracker.NewSafeProvider(inner, "my-tracker")

	err := sp.DeleteIssue(context.Background(), "ENG-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "my-tracker")
}

func TestSafeProvider_LinkIssuesForwarded(t *testing.T) {
	// Linking is additive like commenting — safe mode must not block it.
	var gotKey, gotOther string
	inner := &mockProvider{
		linkIssuesFn: func(_ context.Context, key, otherKey string) error {
			gotKey, gotOther = key, otherKey
			return nil
		},
	}
	sp := tracker.NewSafeProvider(inner, "test-instance")

	err := sp.LinkIssues(context.Background(), "KAN-1", "KAN-2")
	require.NoError(t, err)
	assert.Equal(t, "KAN-1", gotKey)
	assert.Equal(t, "KAN-2", gotOther)
}
