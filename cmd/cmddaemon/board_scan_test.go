package cmddaemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	client "github.com/gethuman-sh/human-daemon-client"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// stubProvider implements tracker.Provider; only ListComments returns data, the
// rest satisfy the interface so the stub can sit in tracker.Instance.Provider.
type stubProvider struct {
	comments []tracker.Comment
}

func (s *stubProvider) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) GetIssue(context.Context, string) (*tracker.Issue, error) { return nil, nil }
func (s *stubProvider) CreateIssue(context.Context, *tracker.Issue) (*tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return s.comments, nil
}
func (s *stubProvider) AddComment(context.Context, string, string) (*tracker.Comment, error) {
	return nil, nil
}
func (s *stubProvider) DeleteIssue(context.Context, string) error             { return nil }
func (s *stubProvider) TransitionIssue(context.Context, string, string) error { return nil }
func (s *stubProvider) AssignIssue(context.Context, string, string) error     { return nil }
func (s *stubProvider) GetCurrentUser(context.Context) (string, error)        { return "", nil }
func (s *stubProvider) EditIssue(context.Context, string, tracker.EditOptions) (*tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) ListStatuses(context.Context, string) ([]tracker.Status, error) {
	return nil, nil
}

func TestScanReadyForReview_BoardCards(t *testing.T) {
	t0 := time.Unix(1000, 0)
	prov := &stubProvider{comments: []tracker.Comment{
		{Body: "[human:plan-ready]\nengineering: HUM-7", Created: t0},
	}}
	jobs := []fetchJob{{inst: tracker.Instance{Name: "work", Kind: "shortcut", Provider: prov}}}
	results := []daemon.TrackerIssuesResult{{
		TrackerRole: "pm",
		Issues:      []client.Issue{{Key: "SC-1", StatusType: client.CategoryUnstarted}},
	}}

	_, _, cards := scanReadyForReview(jobs, results)
	require.Contains(t, cards, "SC-1")
	assert.Equal(t, daemon.BoardPlanning, cards["SC-1"].Stage)
	assert.Equal(t, daemon.BoardDone, cards["SC-1"].State)
	assert.Equal(t, "HUM-7", cards["SC-1"].EngineeringKey)
}
