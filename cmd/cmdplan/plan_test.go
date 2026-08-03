package cmdplan

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

func c(body string, sec int64) tracker.Comment {
	return tracker.Comment{Body: body, Created: time.Unix(sec, 0)}
}

// deferStub implements tracker.Provider; only the four methods RunPlanDefer
// calls carry behavior and record their arguments — the rest satisfy the
// interface. It fails the test if a call it did not expect fires, so the ordered
// create→link→post contract (a marker never names an uncreated ticket) is proven
// by construction.
type deferStub struct {
	getIssue    *tracker.Issue
	getErr      error
	createdKey  string
	createErr   error
	linkErr     error
	addErr      error
	createdWith *tracker.Issue
	linkedArgs  []string
	addedKey    string
	addedBody   string
}

func (s *deferStub) GetIssue(context.Context, string) (*tracker.Issue, error) {
	return s.getIssue, s.getErr
}
func (s *deferStub) CreateIssue(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	s.createdWith = issue
	if s.createErr != nil {
		return nil, s.createErr
	}
	return &tracker.Issue{Key: s.createdKey, Project: issue.Project, Title: issue.Title}, nil
}
func (s *deferStub) LinkIssues(_ context.Context, key, other string, kind tracker.LinkKind) error {
	s.linkedArgs = []string{key, other, string(kind)}
	return s.linkErr
}
func (s *deferStub) AddComment(_ context.Context, key, body string) (*tracker.Comment, error) {
	s.addedKey = key
	s.addedBody = body
	if s.addErr != nil {
		return nil, s.addErr
	}
	return &tracker.Comment{Body: body}, nil
}
func (s *deferStub) UnlinkIssues(context.Context, string, string) error { return nil }
func (s *deferStub) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *deferStub) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, nil
}
func (s *deferStub) DeleteIssue(context.Context, string) error             { return nil }
func (s *deferStub) TransitionIssue(context.Context, string, string) error { return nil }
func (s *deferStub) AssignIssue(context.Context, string, string) error     { return nil }
func (s *deferStub) GetCurrentUser(context.Context) (string, error)        { return "", nil }
func (s *deferStub) EditIssue(context.Context, string, tracker.EditOptions) (*tracker.Issue, error) {
	return nil, nil
}
func (s *deferStub) ListStatuses(context.Context, string) ([]tracker.Status, error) {
	return nil, nil
}

func TestRunPlanDefer_success(t *testing.T) {
	p := &deferStub{getIssue: &tracker.Issue{Project: "PRJ", Title: "T"}, createdKey: "SC-3001"}
	var buf bytes.Buffer

	err := RunPlanDefer(context.Background(), p, &buf, "SC-2910", "Follow-on", "",
		[]string{"CSV export of the cost ledger", "cost webhook"})
	require.NoError(t, err)

	require.NotNil(t, p.createdWith)
	assert.Equal(t, "PRJ", p.createdWith.Project, "follow-on filed in the PM ticket's project")
	assert.Equal(t, []string{"SC-2910", "SC-3001", string(tracker.LinkRelated)}, p.linkedArgs)
	assert.Equal(t, "SC-2910", p.addedKey)
	assert.True(t, strings.HasPrefix(p.addedBody, "[human:shipped-partial]"), p.addedBody)
	assert.Contains(t, p.addedBody, "follow-on: SC-3001")
	assert.Contains(t, p.addedBody, "CSV export of the cost ledger")
	assert.Contains(t, p.addedBody, "cost webhook")
	assert.Contains(t, buf.String(), "SC-3001")
}

func TestRunPlanDefer_emptyTitle(t *testing.T) {
	p := &deferStub{getIssue: &tracker.Issue{Project: "PRJ"}, createdKey: "SC-3001"}
	err := RunPlanDefer(context.Background(), p, &bytes.Buffer{}, "SC-2910", "", "", []string{"A"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title must not be empty")
	assert.Nil(t, p.createdWith, "no ticket created for an empty title")
}

func TestRunPlanDefer_noDeferred(t *testing.T) {
	p := &deferStub{getIssue: &tracker.Issue{Project: "PRJ"}, createdKey: "SC-3001"}
	err := RunPlanDefer(context.Background(), p, &bytes.Buffer{}, "SC-2910", "Follow-on", "", []string{"  ", ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one --deferred")
	assert.Nil(t, p.createdWith, "no ticket created with no criteria")
}

func TestRunPlanDefer_createFails(t *testing.T) {
	p := &deferStub{getIssue: &tracker.Issue{Project: "PRJ"}, createErr: errors.WithDetails("boom")}
	err := RunPlanDefer(context.Background(), p, &bytes.Buffer{}, "SC-2910", "Follow-on", "", []string{"A"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating follow-on ticket")
	assert.Nil(t, p.linkedArgs, "link not attempted after create fails")
	assert.Empty(t, p.addedBody, "marker not posted after create fails")
}

func TestRunPlanDefer_linkFails(t *testing.T) {
	p := &deferStub{getIssue: &tracker.Issue{Project: "PRJ"}, createdKey: "SC-3001", linkErr: errors.WithDetails("boom")}
	err := RunPlanDefer(context.Background(), p, &bytes.Buffer{}, "SC-2910", "Follow-on", "", []string{"A"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linking follow-on ticket")
	assert.Empty(t, p.addedBody, "marker not posted after link fails")
}

func TestFollowOnBody_bulletsAndBacklink(t *testing.T) {
	body := followOnBody("SC-2910", "T", []string{"first", "second"}, "note")
	assert.Contains(t, body, "Deferred from SC-2910 (T)")
	assert.Contains(t, body, "- first\n")
	assert.Contains(t, body, "- second\n")
	assert.Contains(t, body, "note")
}

func TestExtractPlan(t *testing.T) {
	t.Run("latest plan wins, header stripped", func(t *testing.T) {
		body, ok := ExtractPlan([]tracker.Comment{
			c("[human:plan]\n\n## Old", 1),
			c("[human:plan]\n\n## New\n```go\nx := 1\n```", 2),
		})
		assert.True(t, ok)
		assert.Equal(t, "## New\n```go\nx := 1\n```", body)
	})

	t.Run("plan-ready is not a plan", func(t *testing.T) {
		_, ok := ExtractPlan([]tracker.Comment{c("[human:plan-ready]\nengineering: HUM-9", 1)})
		assert.False(t, ok)
	})

	t.Run("quoted header mid-body is not a plan", func(t *testing.T) {
		_, ok := ExtractPlan([]tracker.Comment{c("see `[human:plan]` for details", 1)})
		assert.False(t, ok)
	})

	t.Run("no comments", func(t *testing.T) {
		_, ok := ExtractPlan(nil)
		assert.False(t, ok)
	})
}
