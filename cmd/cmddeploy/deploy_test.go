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
	overrideDecision             bool
}

// stubEngine swaps deployEntry rather than the engine underneath it: the seam
// this route now calls first is the pre-deploy prelude (StartDeploy), so a
// test exercising RunDeploy's derivation stubs at that boundary.
func stubEngine(t *testing.T, err error) *[]engineCall {
	t.Helper()
	var calls []engineCall
	prevEntry, prevDeps := deployEntry, newTransitionDeps
	deployEntry = func(_ context.Context, _ daemon.BoardTransitionDeps, req daemon.StartDeployRequest) (daemon.StartDeployResult, error) {
		calls = append(calls, engineCall{req.PMKey, req.Title, req.PRBody, req.Branch, req.OverrideDecision})
		return daemon.StartDeployResult{}, err
	}
	newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps {
		return daemon.BoardTransitionDeps{}
	}
	t.Cleanup(func() { deployEntry, newTransitionDeps = prevEntry, prevDeps })
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
func (s *stubDeployer) FreshenBranch(context.Context, daemon.PRRequest) (daemon.BranchFreshness, error) {
	return daemon.FreshnessCurrent, nil
}

func (s *stubDeployer) EnsureMergeable(context.Context, daemon.PRRequest) (string, error) {
	return "", nil
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
		return daemon.BoardTransitionDeps{Commenter: p, Deployer: &stubDeployer{events: events}, Launcher: stubLauncher{}, Logger: zerolog.Nop(), WorkspaceDir: "."}
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, nil)

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

	require.NoError(t, RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, nil))

	require.NotEmpty(t, *events)
	assert.Equal(t, "comment:[human:deploy-started]", (*events)[0],
		"the start is recorded on the ticket before the engine touches the forge")
	assert.Contains(t, p.posted[0], "branch: feat/x")
}

// A person may decide to ship past their own open question. --override-decision
// is that explicit decision — it must still record the start and still run the
// engine, it just skips the refusal.
func TestRunDeploy_overrideShipsWhileADecisionIsOpen(t *testing.T) {
	p, events := realRoute(t, []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x\ncommits: abc", Created: time.Now()},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", Created: time.Now()},
	})
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, true, nil)

	require.NoError(t, err)
	assert.Contains(t, *events, "engine")
	require.NotEmpty(t, p.posted)
	assert.Contains(t, p.posted[0], "[human:deploy-started]")
}

// RunDeploy's own job is derivation; whether to override is the caller's flag,
// passed straight through to the entry point untouched.
func TestRunDeploy_passesTheOverrideThroughToTheEntryPoint(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "T", false, true, nil)

	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.True(t, (*calls)[0].overrideDecision)
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, nil)
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "Custom title", false, false, nil)
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.Equal(t, "release/x", (*calls)[0].branch)
	assert.Equal(t, "Custom title", (*calls)[0].title)
}

func TestRunDeploy_noHandoffNoBranchFails(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, nil)
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

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no branch")
	assert.Empty(t, *calls)
}

func TestRunDeploy_engineErrorPropagates(t *testing.T) {
	stubEngine(t, errors.WithDetails("deploy failed: CI checks failed"))
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "release/x", "T", false, false, nil)
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

// --ready (the review loop's draft interlock, SC-4027) and --override-decision
// (the open-decision refusal, SC-3852) are independent gestures that reach the
// engine by different carriers: the draft override rides on the deps, the
// decision override on the request. Routing the CLI through StartDeploy moved
// the call that carries the deps, so pin that --ready still arrives.
func TestRunDeploy_readyCarriesTheDraftOverrideThroughThePrelude(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready bool
	}{{"ready un-drafts", true}, {"default leaves the interlock", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var got daemon.BoardTransitionDeps
			prevEntry, prevDeps := deployEntry, newTransitionDeps
			deployEntry = func(_ context.Context, d daemon.BoardTransitionDeps, _ daemon.StartDeployRequest) (daemon.StartDeployResult, error) {
				got = d
				return daemon.StartDeployResult{}, nil
			}
			newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps {
				return daemon.BoardTransitionDeps{}
			}
			t.Cleanup(func() { deployEntry, newTransitionDeps = prevEntry, prevDeps })

			var buf bytes.Buffer
			require.NoError(t, RunDeploy(context.Background(), &stubProvider{}, &buf,
				"SC-1", "release/x", "T", tc.ready, false, nil))
			assert.Equal(t, tc.ready, got.MergeDraftPR)
		})
	}
}

// stubLauncher stands in for the daemon's launcher so the production route can
// enter the review; it never actually starts anything.
type stubLauncher struct{}

func (stubLauncher) Launch(context.Context, string, string, string, string, string) error { return nil }

// Forwarded to the daemon, the command must use the daemon's own engine wiring
// — that is what carries the launcher the machine review needs. The bare
// wiring is only for a run with no daemon at all.
func TestTransitionDepsFor_prefersTheDaemonsWiring(t *testing.T) {
	prev := newTransitionDeps
	newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps {
		return daemon.BoardTransitionDeps{WorkspaceDir: "bare"}
	}
	t.Cleanup(func() { newTransitionDeps = prev })
	resolver := daemon.TransitionDepsResolver(func(pmKey string) (daemon.BoardTransitionDeps, error) {
		return daemon.BoardTransitionDeps{WorkspaceDir: "daemon:" + pmKey}, nil
	})

	got := transitionDepsFor(daemon.WithTransitionDeps(context.Background(), resolver), &stubProvider{}, "SC-1")
	assert.Equal(t, "daemon:SC-1", got.WorkspaceDir)

	bare := transitionDepsFor(context.Background(), &stubProvider{}, "SC-1")
	assert.Equal(t, "bare", bare.WorkspaceDir, "no daemon on the context means the bare wiring")

	failing := daemon.TransitionDepsResolver(func(string) (daemon.BoardTransitionDeps, error) {
		return daemon.BoardTransitionDeps{}, errors.WithDetails("no project for key")
	})
	fallback := transitionDepsFor(daemon.WithTransitionDeps(context.Background(), failing), &stubProvider{}, "SC-1")
	assert.Equal(t, "bare", fallback.WorkspaceDir, "a resolver that cannot place the key falls back rather than failing the command")
}

// The two outcomes leave the work in different places, and the line a person
// reads must say which: "Deployed" for work held in a draft the review has
// not approved would be the record misstating the outcome.
func TestRunDeploy_reportsAStartedReviewAsSuch(t *testing.T) {
	prevEntry, prevDeps := deployEntry, newTransitionDeps
	deployEntry = func(context.Context, daemon.BoardTransitionDeps, daemon.StartDeployRequest) (daemon.StartDeployResult, error) {
		return daemon.StartDeployResult{Outcome: daemon.DeployOutcomeReviewStarted, PRURL: "https://example/pr/9", PRNumber: 9}, nil
	}
	newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps { return daemon.BoardTransitionDeps{} }
	t.Cleanup(func() { deployEntry, newTransitionDeps = prevEntry, prevDeps })
	var buf bytes.Buffer

	require.NoError(t, RunDeploy(context.Background(), &stubProvider{}, &buf, "SC-1", "release/x", "T", false, false, nil))

	assert.Contains(t, buf.String(), "Review started for SC-1 (release/x): https://example/pr/9")
	assert.NotContains(t, buf.String(), "Deployed")
}

// A fixer dispatched before any reviewer runs (a stale-base conflict, or a
// clean merge that leaves the fast test tier red) must not be reported as a
// review in progress: that would tell a person watching `human deploy` that
// the reviewer is working when the fixer is (SC-5279).
func TestRunDeploy_reportsAFixDispatchNotAReview(t *testing.T) {
	prevEntry, prevDeps := deployEntry, newTransitionDeps
	deployEntry = func(context.Context, daemon.BoardTransitionDeps, daemon.StartDeployRequest) (daemon.StartDeployResult, error) {
		return daemon.StartDeployResult{Outcome: daemon.DeployOutcomeFixDispatched, PRURL: "https://example/pr/9", PRNumber: 9}, nil
	}
	newTransitionDeps = func(tracker.Provider) daemon.BoardTransitionDeps { return daemon.BoardTransitionDeps{} }
	t.Cleanup(func() { deployEntry, newTransitionDeps = prevEntry, prevDeps })
	var buf bytes.Buffer

	require.NoError(t, RunDeploy(context.Background(), &stubProvider{}, &buf, "SC-1", "release/x", "T", false, false, nil))

	assert.Contains(t, buf.String(), "https://example/pr/9")
	assert.NotContains(t, buf.String(), "Review started")
	assert.NotContains(t, buf.String(), "Deployed")
}

// Without a handoff, the one branch the caller's checkout found carrying the
// ticket's commits is the branch to ship (SC-5330).
func TestRunDeploy_noHandoffUsesTheSingleCandidateBranch(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{issue: &tracker.Issue{Key: "SC-1", Title: "T"}}
	var buf bytes.Buffer

	require.NoError(t, RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, []string{"fix/sc-1"}))

	require.Len(t, *calls, 1)
	assert.Equal(t, "fix/sc-1", (*calls)[0].branch)
}

// Several candidates are a question the gate must not answer by itself; the
// refusal names them so the caller can.
func TestRunDeploy_noHandoffSeveralCandidatesRefusesNamingThem(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, []string{"fix/a", "fix/b"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "several branches")
	assert.Equal(t, "fix/a, fix/b", errors.AllDetails(err)["branches"])
	assert.Empty(t, *calls)
}

// A recorded handoff keeps precedence over whatever the caller's checkout
// found: the handoff is the reviewed binding.
func TestRunDeploy_handoffOutranksCandidates(t *testing.T) {
	calls := stubEngine(t, nil)
	p := &stubProvider{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x\ncommits: abc", Created: time.Now()},
	}, issue: &tracker.Issue{Key: "SC-1", Title: "T"}}
	var buf bytes.Buffer

	require.NoError(t, RunDeploy(context.Background(), p, &buf, "SC-1", "", "", false, false, []string{"fix/other"}))

	require.Len(t, *calls, 1)
	assert.Equal(t, "feat/x", (*calls)[0].branch)
}
