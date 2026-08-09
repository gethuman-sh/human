package cmddeploy

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/forge"
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
func (s *stubProvider) LinkIssues(context.Context, string, string, tracker.LinkKind) error {
	return nil
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

// recordingProvider records the ORDER of what the deploy route does to the
// ticket, which is the whole question: a start recorded after the merge is not
// a record of the work starting.
type recordingProvider struct {
	stubProvider
	mu     sync.Mutex
	posted []string // marker bodies, in the order they were posted
	events *[]string
}

func (r *recordingProvider) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posted = append(r.posted, body)
	*r.events = append(*r.events, "comment:"+strings.SplitN(body, "\n", 2)[0])
	return &tracker.Comment{Body: body}, nil
}

// stubDeployer implements daemon.Deployer; every method returns zero values
// except BranchMerged (the engine's first Deployer call, so it is the probe
// for "the engine ran") and PushAndCreatePR (belt and braces).
type stubDeployer struct {
	events *[]string
}

func (s *stubDeployer) PushAndCreatePR(context.Context, daemon.PRRequest) (daemon.PRResult, error) {
	*s.events = append(*s.events, "engine")
	return daemon.PRResult{Number: 1, URL: "pr"}, nil
}
func (s *stubDeployer) PullRequestChecks(context.Context, string, int) (forge.ChecksState, error) {
	return "", nil
}
func (s *stubDeployer) ReadPullRequest(context.Context, string, int) (*forge.PullRequestState, error) {
	return nil, nil
}
func (s *stubDeployer) EnsureMergeable(context.Context, daemon.PRRequest) (bool, error) {
	return false, nil
}
func (s *stubDeployer) PullRequestMergeable(context.Context, string, int) (bool, error) {
	return true, nil
}
func (s *stubDeployer) MergePullRequest(context.Context, string, int) error { return nil }
func (s *stubDeployer) DeleteRemoteBranch(context.Context, string, string) error {
	return nil
}
func (s *stubDeployer) BranchMerged(context.Context, string, string) bool {
	*s.events = append(*s.events, "engine")
	return true
}
func (s *stubDeployer) MarkReadyForReview(context.Context, string, int) error { return nil }
func (s *stubDeployer) PublishResolvedBranch(context.Context, string, string) (bool, error) {
	return false, nil
}

// realRoute leaves deployEntry ALONE — the point is to drive production wiring
// with only the forge replaced.
func realRoute(t *testing.T, comments []tracker.Comment) (*recordingProvider, *[]string) {
	t.Helper()
	events := &[]string{}
	p := &recordingProvider{stubProvider: stubProvider{comments: comments,
		issue: &tracker.Issue{Key: "SC-1", Title: "T"}}, events: events}
	prev := newTransitionDeps
	newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps {
		return daemon.BoardTransitionDeps{Commenter: p, Deployer: &stubDeployer{events: events}, Logger: zerolog.Nop(), WorkspaceDir: "."}
	}
	t.Cleanup(func() { newTransitionDeps = prev })
	return p, events
}

// A ticket paused on an open decision is the one state nothing may move. The CLI
// deploy ships it today, which is the bug.
func TestRunDeploy_refusesWhileADecisionIsOpen(t *testing.T) {
	p, events := realRoute(t, []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x\ncommits: abc", Created: time.Now()},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", Created: time.Now()},
	})
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploy refused")
	assert.NotContains(t, *events, "engine", "nothing may be shipped while a decision is open")
	assert.Empty(t, p.posted, "a refusal is not a failure: it posts no marker at all")
	assert.NotContains(t, buf.String(), "Deployed")
}

// The ticket's own record of a CLI deploy must begin before the merge.
func TestRunDeploy_recordsTheStartBeforeTheMerge(t *testing.T) {
	p, events := realRoute(t, []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x\ncommits: abc", Created: time.Now()},
	})
	var buf bytes.Buffer

	require.NoError(t, RunDeploy(context.Background(), p, &buf, "SC-1", "", ""))

	require.NotEmpty(t, *events)
	assert.Equal(t, "comment:[human:deploy-started]", (*events)[0],
		"the start is recorded on the ticket before the engine touches the forge")
	assert.Contains(t, p.posted[0], "branch: feat/x")
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false)
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "Custom title", false)
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.Equal(t, "release/x", (*calls)[0].branch)
	assert.Equal(t, "Custom title", (*calls)[0].title)
}

func TestRunDeploy_noHandoffNoBranchFails(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false)
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no branch")
	assert.Empty(t, *calls)
}

func TestRunDeploy_engineErrorPropagates(t *testing.T) {
	stubEngine(t, errors.WithDetails("deploy failed: CI checks failed"))
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "T", false)
	require.Error(t, err)
	assert.NotContains(t, buf.String(), "Deployed")
}

func TestPRBody_singleTrackerOmitsEngineering(t *testing.T) {
	body := prBody("SC-1", "", "autofix/sc-1")
	assert.Contains(t, body, "PM ticket: SC-1")
	assert.NotContains(t, body, "Engineering ticket")
	assert.Contains(t, body, "Branch: autofix/sc-1")
}

func (s *stubProvider) UnlinkIssues(context.Context, string, string) error {
	return nil
}
