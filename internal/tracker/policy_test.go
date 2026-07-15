package tracker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestPolicy_BlockDelete(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"delete"},
	})
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("delete", ""))
}

func TestPolicy_ConfirmCreate(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Confirm: []string{"create"},
	})
	assert.Equal(t, tracker.ActionConfirm, p.Evaluate("create", ""))
}

func TestPolicy_AllowUnlisted(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Block:   []string{"delete"},
		Confirm: []string{"create"},
	})
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("edit", ""))
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("assign", ""))
}

func TestPolicy_ParameterizedTransition(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"transition:Done"},
	})
	// Matches the parameterized rule.
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("transition", "Done"))
	// Does not match a different argument.
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("transition", "In Progress"))
	// Does not match without an argument.
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("transition", ""))
}

func TestPolicy_ParameterizedConfirm(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Confirm: []string{"transition:Done"},
	})
	assert.Equal(t, tracker.ActionConfirm, p.Evaluate("transition", "Done"))
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("transition", "In Progress"))
}

func TestPolicy_CaseInsensitive(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Block:   []string{"Delete"},
		Confirm: []string{"Transition:DONE"},
	})
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("DELETE", ""))
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("delete", ""))
	assert.Equal(t, tracker.ActionConfirm, p.Evaluate("transition", "done"))
	assert.Equal(t, tracker.ActionConfirm, p.Evaluate("TRANSITION", "Done"))
}

func TestPolicy_BlockTakesPrecedenceOverConfirm(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Block:   []string{"delete"},
		Confirm: []string{"delete"},
	})
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("delete", ""))
}

func TestPolicy_BareRuleMatchesAllArguments(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"transition"},
	})
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("transition", "Done"))
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("transition", "In Progress"))
	assert.Equal(t, tracker.ActionBlock, p.Evaluate("transition", ""))
}

func TestPolicy_EmptyConfig(t *testing.T) {
	p := tracker.NewPolicy(tracker.PolicyConfig{})
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("delete", ""))
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("create", ""))
	assert.Equal(t, tracker.ActionAllow, p.Evaluate("transition", "Done"))
}

func TestPolicyProvider_BlockReturnsError(t *testing.T) {
	deleteCalled := false
	inner := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			deleteCalled = true
			return nil
		},
	}

	policy := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"delete"},
	})
	pp := tracker.NewPolicyProvider(inner, "prod-jira", policy, nil)

	err := pp.DeleteIssue(context.Background(), "KAN-5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation blocked by policy")
	assert.Contains(t, err.Error(), "prod-jira")
	assert.False(t, deleteCalled, "inner DeleteIssue should not be called")
}

func TestPolicyProvider_BlockCreateReturnsError(t *testing.T) {
	createCalled := false
	inner := &mockProvider{
		createIssueFn: func(_ context.Context, _ *tracker.Issue) (*tracker.Issue, error) {
			createCalled = true
			return &tracker.Issue{Key: "KAN-99"}, nil
		},
	}

	policy := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"create"},
	})
	pp := tracker.NewPolicyProvider(inner, "prod-jira", policy, nil)

	_, err := pp.CreateIssue(context.Background(), &tracker.Issue{Project: "KAN", Title: "New"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation blocked by policy")
	assert.False(t, createCalled)
}

func TestPolicyProvider_ConfirmCallsWarnAndProceeds(t *testing.T) {
	createCalled := false
	inner := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			createCalled = true
			return &tracker.Issue{Key: "KAN-99", Project: issue.Project}, nil
		},
	}

	var warnMsg string
	policy := tracker.NewPolicy(tracker.PolicyConfig{
		Confirm: []string{"create"},
	})
	pp := tracker.NewPolicyProvider(inner, "prod-jira", policy, func(msg string) {
		warnMsg = msg
	})

	created, err := pp.CreateIssue(context.Background(), &tracker.Issue{Project: "KAN", Title: "New"})
	require.NoError(t, err)
	assert.Equal(t, "KAN-99", created.Key)
	assert.True(t, createCalled, "inner CreateIssue should be called")
	assert.Contains(t, warnMsg, "create")
	assert.Contains(t, warnMsg, "prod-jira")
}

func TestPolicyProvider_ConfirmTransitionIncludesArgument(t *testing.T) {
	inner := &mockProvider{
		transitionIssueFn: func(_ context.Context, _ string, _ string) error {
			return nil
		},
	}

	var warnMsg string
	policy := tracker.NewPolicy(tracker.PolicyConfig{
		Confirm: []string{"transition:Done"},
	})
	pp := tracker.NewPolicyProvider(inner, "my-tracker", policy, func(msg string) {
		warnMsg = msg
	})

	err := pp.TransitionIssue(context.Background(), "KAN-1", "Done")
	require.NoError(t, err)
	assert.Contains(t, warnMsg, "transition:Done")
	assert.Contains(t, warnMsg, "my-tracker")
}

func TestPolicyProvider_ReadMethodsAlwaysPassThrough(t *testing.T) {
	inner := &mockProvider{
		listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key}, nil
		},
		listCommentsFn: func(_ context.Context, _ string) ([]tracker.Comment, error) {
			return []tracker.Comment{{ID: "c-1"}}, nil
		},
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-1", nil
		},
		listStatusesFn: func(_ context.Context, _ string) ([]tracker.Status, error) {
			return []tracker.Status{{Name: "To Do"}}, nil
		},
	}

	// Block everything -- read methods should still pass through.
	policy := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"delete", "create", "assign", "edit", "comment", "transition"},
	})
	pp := tracker.NewPolicyProvider(inner, "test", policy, nil)
	ctx := context.Background()

	issues, err := pp.ListIssues(ctx, tracker.ListOptions{Project: "KAN"})
	require.NoError(t, err)
	assert.Len(t, issues, 1)

	issue, err := pp.GetIssue(ctx, "KAN-1")
	require.NoError(t, err)
	assert.Equal(t, "KAN-1", issue.Key)

	comments, err := pp.ListComments(ctx, "KAN-1")
	require.NoError(t, err)
	assert.Len(t, comments, 1)

	userID, err := pp.GetCurrentUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "user-1", userID)

	statuses, err := pp.ListStatuses(ctx, "KAN-1")
	require.NoError(t, err)
	assert.Len(t, statuses, 1)
}

func TestPolicyProvider_AllWriteMethodsChecked(t *testing.T) {
	inner := &mockProvider{
		createIssueFn: func(_ context.Context, _ *tracker.Issue) (*tracker.Issue, error) {
			return &tracker.Issue{Key: "KAN-99"}, nil
		},
		deleteIssueFn: func(_ context.Context, _ string) error {
			return nil
		},
		addCommentFn: func(_ context.Context, _, _ string) (*tracker.Comment, error) {
			return &tracker.Comment{ID: "c-1"}, nil
		},
		transitionIssueFn: func(_ context.Context, _, _ string) error {
			return nil
		},
		assignIssueFn: func(_ context.Context, _, _ string) error {
			return nil
		},
		editIssueFn: func(_ context.Context, _ string, _ tracker.EditOptions) (*tracker.Issue, error) {
			return &tracker.Issue{Key: "KAN-1"}, nil
		},
	}

	policy := tracker.NewPolicy(tracker.PolicyConfig{
		Block: []string{"create", "delete", "comment", "transition", "assign", "edit"},
	})
	pp := tracker.NewPolicyProvider(inner, "test", policy, nil)
	ctx := context.Background()

	_, err := pp.CreateIssue(ctx, &tracker.Issue{})
	assert.Error(t, err)

	err = pp.DeleteIssue(ctx, "KAN-1")
	assert.Error(t, err)

	_, err = pp.AddComment(ctx, "KAN-1", "hello")
	assert.Error(t, err)

	err = pp.TransitionIssue(ctx, "KAN-1", "Done")
	assert.Error(t, err)

	err = pp.AssignIssue(ctx, "KAN-1", "user-1")
	assert.Error(t, err)

	_, err = pp.EditIssue(ctx, "KAN-1", tracker.EditOptions{})
	assert.Error(t, err)
}

func TestPolicyProvider_LinkIssues(t *testing.T) {
	inner := &mockProvider{
		linkIssuesFn: func(_ context.Context, _, _ string) error { return nil },
	}

	blocked := tracker.NewPolicyProvider(inner, "test",
		tracker.NewPolicy(tracker.PolicyConfig{Block: []string{"link"}}), nil)
	err := blocked.LinkIssues(context.Background(), "KAN-1", "KAN-2")
	assert.Error(t, err)

	allowed := tracker.NewPolicyProvider(inner, "test",
		tracker.NewPolicy(tracker.PolicyConfig{}), nil)
	err = allowed.LinkIssues(context.Background(), "KAN-1", "KAN-2")
	assert.NoError(t, err)
}
