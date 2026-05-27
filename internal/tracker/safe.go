package tracker

import (
	"context"

	"github.com/gethuman-sh/human/errors"
)

// SafeProvider wraps a Provider and blocks mutating operations.
// DeleteIssue, EditIssue, and TransitionIssue are blocked; read operations
// and assignments delegate to the inner provider.
type SafeProvider struct {
	inner        Provider
	instanceName string
}

// NewSafeProvider creates a SafeProvider that delegates to inner and blocks
// DeleteIssue with a descriptive error.
func NewSafeProvider(inner Provider, instanceName string) *SafeProvider {
	return &SafeProvider{inner: inner, instanceName: instanceName}
}

func (s *SafeProvider) ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error) {
	return s.inner.ListIssues(ctx, opts)
}

func (s *SafeProvider) GetIssue(ctx context.Context, key string) (*Issue, error) {
	return s.inner.GetIssue(ctx, key)
}

func (s *SafeProvider) CreateIssue(ctx context.Context, issue *Issue) (*Issue, error) {
	return s.inner.CreateIssue(ctx, issue)
}

func (s *SafeProvider) DeleteIssue(_ context.Context, _ string) error {
	return errors.WithDetails("operation blocked by safe mode: %s on %s",
		"operation", "DeleteIssue",
		"instance", s.instanceName)
}

func (s *SafeProvider) ListComments(ctx context.Context, issueKey string) ([]Comment, error) {
	return s.inner.ListComments(ctx, issueKey)
}

func (s *SafeProvider) AddComment(ctx context.Context, issueKey string, body string) (*Comment, error) {
	return s.inner.AddComment(ctx, issueKey, body)
}

func (s *SafeProvider) TransitionIssue(_ context.Context, _ string, _ string) error {
	return errors.WithDetails("operation blocked by safe mode: %s on %s",
		"operation", "TransitionIssue",
		"instance", s.instanceName)
}

func (s *SafeProvider) AssignIssue(ctx context.Context, key string, userID string) error {
	return s.inner.AssignIssue(ctx, key, userID)
}

func (s *SafeProvider) GetCurrentUser(ctx context.Context) (string, error) {
	return s.inner.GetCurrentUser(ctx)
}

func (s *SafeProvider) EditIssue(_ context.Context, _ string, _ EditOptions) (*Issue, error) {
	return nil, errors.WithDetails("operation blocked by safe mode: %s on %s",
		"operation", "EditIssue",
		"instance", s.instanceName)
}

func (s *SafeProvider) ListStatuses(ctx context.Context, key string) ([]Status, error) {
	return s.inner.ListStatuses(ctx, key)
}
