package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// fakeCommenter records AddComment bodies and returns canned ListComments.
type fakeCommenter struct {
	comments []tracker.Comment
	added    []string
	addErr   error
}

func (f *fakeCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	return f.comments, nil
}

func (f *fakeCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.added = append(f.added, body)
	c := tracker.Comment{Body: body, Created: time.Now()}
	f.comments = append(f.comments, c)
	return &c, nil
}

// fakeLauncher records the launch name/prompt and can fail.
type fakeLauncher struct {
	name   string
	prompt string
	err    error
	calls  int
}

func (f *fakeLauncher) Launch(_ context.Context, name, prompt, _, _ string) error {
	f.calls++
	f.name = name
	f.prompt = prompt
	return f.err
}

// fakePublisher returns a canned URL or error.
type fakePublisher struct {
	url  string
	err  error
	req  PRRequest
	call int
}

func (f *fakePublisher) PushAndCreatePR(_ context.Context, req PRRequest) (string, error) {
	f.call++
	f.req = req
	return f.url, f.err
}

func newDeps(c *fakeCommenter, l *fakeLauncher, p *fakePublisher) BoardTransitionDeps {
	return BoardTransitionDeps{Commenter: c, Launcher: l, Publisher: p, WorkspaceDir: "/ws", ConfigDir: "/ws"}
}

func TestApplyTransitionBackwardRejected(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardBacklog})
	require.Error(t, err)
	assert.Empty(t, c.added)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionSkipRejected(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	// Backlog -> Implementation skips Planning.
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionGatedBlock(t *testing.T) {
	// Planning running (not done) must block advancing to Implementation.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionBacklogToPlanning(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.NoError(t, err)
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-plan SC-1", l.prompt)
	assert.Equal(t, "board-SC-1-planning", l.name)
	require.Len(t, c.added, 1)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
}

func TestApplyTransitionPlanningToImplementation(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)
	assert.Equal(t, "/human-execute HUM-9", l.prompt)
	assert.Contains(t, c.added, ImplementationStartedHeader)
}

func TestApplyTransitionImplementationToVerification(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})
	require.NoError(t, err)
	assert.Equal(t, "/human-review HUM-9", l.prompt)
	assert.Contains(t, c.added, ReviewStartedHeader)
}

func TestApplyTransitionIdempotentDuplicate(t *testing.T) {
	// An open started marker for the target stage makes the drop a no-op.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:planning-started]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

func TestApplyTransitionDoneNoBranch(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:review-complete]", time.Unix(1, 0))}}
	p := &fakePublisher{}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	assert.Zero(t, p.call)
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], PRFailedHeader)
}

func TestApplyTransitionDoneSuccess(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakePublisher{url: "https://example/pr/7"}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", PMTitle: "My feature", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Equal(t, 1, p.call)
	assert.Equal(t, "feat/x", p.req.Branch)
	assert.Equal(t, "My feature", p.req.Title)
	assert.Contains(t, c.added, PRStartedHeader)
	assert.Contains(t, c.added, PRPushedHeader+"\npr: https://example/pr/7")
}

func TestApplyTransitionDonePushFails(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Unix(1, 0)),
		cmt("[human:review-complete]", time.Unix(2, 0)),
	}}
	p := &fakePublisher{err: errors.New("push rejected")}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)
	var sawFailed bool
	for _, b := range c.added {
		if len(b) >= len(PRFailedHeader) && b[:len(PRFailedHeader)] == PRFailedHeader {
			sawFailed = true
		}
	}
	assert.True(t, sawFailed)
}

func TestStartAgentStageLaunchFails(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{err: errors.New("docker down")}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.Error(t, err)
	// started marker posted, then failed marker posted on launch error.
	require.Len(t, c.added, 2)
	assert.Equal(t, PlanningStartedHeader, c.added[0])
	assert.Contains(t, c.added[1], PlanningFailedHeader)
}

func TestAgentNameRoundTrip(t *testing.T) {
	name := agentNameFor("SC-105", BoardImplementation)
	assert.Equal(t, "board-SC-105-implementation", name)
	pm, stage, ok := parseAgentName(name)
	require.True(t, ok)
	assert.Equal(t, "SC-105", pm)
	assert.Equal(t, BoardImplementation, stage)
}

func TestParseAgentNameRejectsMalformed(t *testing.T) {
	cases := []string{
		"agent-1",       // wrong prefix
		"board-",        // no key/stage
		"board-onlykey", // no trailing stage segment
		"board--done",   // empty key segment
	}
	for _, name := range cases {
		_, _, ok := parseAgentName(name)
		assert.False(t, ok, "name %q should not parse", name)
	}
}

// listErrCommenter fails ListComments to exercise ApplyTransition's load-error path.
type listErrCommenter struct{ *fakeCommenter }

func (listErrCommenter) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, errors.New("tracker unreachable")
}

func TestApplyTransitionListCommentsError(t *testing.T) {
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, &fakePublisher{})
	deps.Commenter = listErrCommenter{&fakeCommenter{}}
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.Error(t, err)
}

func TestStartAgentStageStartedMarkerError(t *testing.T) {
	// AddComment failing on the started marker aborts before launch.
	c := &fakeCommenter{addErr: errors.New("comment api down")}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", To: BoardPlanning})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestFailedHeaderFor(t *testing.T) {
	assert.Equal(t, PlanningFailedHeader, failedHeaderFor(BoardPlanning))
	assert.Equal(t, ImplementationFailedHeader, failedHeaderFor(BoardImplementation))
	assert.Equal(t, ReviewFailedHeader, failedHeaderFor(BoardVerification))
	assert.Equal(t, PRFailedHeader, failedHeaderFor(BoardDoneStage))
	assert.Equal(t, "", failedHeaderFor(BoardBacklog))
}

func TestApplyTransitionMissingEngineeringKey(t *testing.T) {
	// plan-ready without an engineering: line leaves EngineeringKey empty, so
	// advancing to Implementation must error before launching.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:plan-ready]", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}

func TestApplyTransitionVerificationMissingEngineeringKey(t *testing.T) {
	// An implementation done-marker that carries no engineering: line blocks
	// the Verification launch.
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:ready-for-review]\nbranch: feat/x", time.Unix(1, 0))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakePublisher{})
	err := deps.ApplyTransition(context.Background(), BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})
	require.Error(t, err)
	assert.Zero(t, l.calls)
}
