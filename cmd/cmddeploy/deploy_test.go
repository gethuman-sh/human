package cmddeploy

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// stubProvider implements tracker.Provider; comments and issue fetch matter.
type stubProvider struct {
	comments []tracker.Comment
	issue    *tracker.Issue
}

func (s *stubProvider) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) GetIssue(context.Context, string) (*tracker.Issue, error) {
	return s.issue, nil
}
func (s *stubProvider) CreateIssue(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	return issue, nil
}
func (s *stubProvider) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return s.comments, nil
}
func (s *stubProvider) AddComment(context.Context, string, string) (*tracker.Comment, error) {
	return nil, nil
}
func (s *stubProvider) LinkIssues(context.Context, string, string) error      { return nil }
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

type engineCall struct {
	pmKey, title, prBody, branch string
}

func stubEngine(t *testing.T, err error) *[]engineCall {
	t.Helper()
	var calls []engineCall
	prevEngine, prevDeps := deployEngine, newTransitionDeps
	deployEngine = func(_ context.Context, _ daemon.BoardTransitionDeps, pmKey, title, prBody, branch string) error {
		calls = append(calls, engineCall{pmKey, title, prBody, branch})
		return err
	}
	newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps {
		return daemon.BoardTransitionDeps{}
	}
	t.Cleanup(func() { deployEngine, newTransitionDeps = prevEngine, prevDeps })
	return &calls
}

func TestRunDeploy_derivesBranchAndTitleFromHandoffAndTicket(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{
		comments: []tracker.Comment{{
			Body:    "[human:ready-for-review]\nengineering: HUM-9\nbranch: autofix/sc-1\ncommits: abc",
			Created: time.Now(),
		}},
		issue: &tracker.Issue{Key: "SC-1", Title: "Fix the thing"},
	}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "")
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	call := (*calls)[0]
	assert.Equal(t, "SC-1", call.pmKey)
	assert.Equal(t, "Fix the thing", call.title)
	assert.Equal(t, "autofix/sc-1", call.branch)
	assert.Contains(t, call.prBody, "PM ticket: SC-1")
	assert.Contains(t, call.prBody, "Engineering ticket: HUM-9")
	assert.Contains(t, buf.String(), "Deployed SC-1 (autofix/sc-1)")
}

func TestRunDeploy_explicitFlagsSkipDerivation(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "Custom title")
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.Equal(t, "release/x", (*calls)[0].branch)
	assert.Equal(t, "Custom title", (*calls)[0].title)
}

func TestRunDeploy_noHandoffNoBranchFails(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no review handoff")
	assert.Empty(t, *calls)
}

func TestRunDeploy_handoffWithoutBranchFails(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{comments: []tracker.Comment{{
		Body:    "[human:ready-for-review]\ncommits: abc",
		Created: time.Now(),
	}}}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no branch")
	assert.Empty(t, *calls)
}

func TestRunDeploy_engineErrorPropagates(t *testing.T) {
	stubEngine(t, errors.WithDetails("deploy failed: CI checks failed"))
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "T")
	require.Error(t, err)
	assert.NotContains(t, buf.String(), "Deployed")
}

func TestPRBody_singleTrackerOmitsEngineering(t *testing.T) {
	body := prBody("SC-1", "", "autofix/sc-1")
	assert.Contains(t, body, "PM ticket: SC-1")
	assert.NotContains(t, body, "Engineering ticket")
	assert.Contains(t, body, "Branch: autofix/sc-1")
}
