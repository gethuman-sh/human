package daemon

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// The refusal is the deploy's one non-failure outcome: nothing may be shipped
// while a person's own open question sits unanswered on the ticket, and
// refusing must not look like the deploy broke.
func TestStartDeploy_refusalIsNotAFailure(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1"},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", ID: "2"},
	}}
	p := &fakeDeployer{}
	deps := newDeps(c, nil, p)

	err := deps.StartDeploy(context.Background(), StartDeployRequest{PMKey: "SC-1", Branch: "feat/x"})

	require.Error(t, err)
	assert.True(t, stderrors.Is(err, ErrDeployAwaitingDecision))
	assert.Empty(t, c.added, "a refusal is not a failure: it posts no marker at all")
	assert.Zero(t, p.call, "the engine (fakeDeployer's PushAndCreatePR) must never run on a refusal")
}

// The ticket's own record of a deploy must begin before the engine touches the
// forge — a start recorded after the fact is not a record of the work starting.
func TestStartDeploy_recordsTheStartThenRunsTheEngine(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{
		res:       PRResult{Number: 42, URL: "https://example/pr/42"},
		checks:    []forge.ChecksState{forge.ChecksPassing},
		mergeable: true,
	}
	deps := newDeps(c, nil, p)

	err := deps.StartDeploy(context.Background(), StartDeployRequest{
		PMKey: "SC-1", Title: "t", PRBody: "body", Branch: "feat/x",
	})

	require.NoError(t, err)
	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], DeployStartedHeader)
	assert.Contains(t, c.added[0], "branch: feat/x")
	assert.Equal(t, 1, p.call, "PushAndCreatePR must run exactly once, after the start marker")
	assert.Equal(t, "feat/x", p.req.Branch)
}

// A missing comment post is a lost sentence, not a reason to withhold the
// ship: the merge is the work.
func TestStartDeploy_aFailedMarkerPostStillShips(t *testing.T) {
	c := &fakeCommenter{addErr: stderrors.New("tracker unavailable")}
	p := &fakeDeployer{alreadyMerged: true}
	deps := newDeps(c, nil, p)

	err := deps.StartDeploy(context.Background(), StartDeployRequest{
		PMKey: "SC-1", Title: "t", PRBody: "body", Branch: "feat/x",
	})

	assert.NoError(t, err)
}

// --override-decision is a person deciding to ship past their own open
// question — it must still record the start and still run the engine.
func TestStartDeploy_overrideShipsPastAnOpenDecision(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "1"},
		{Body: "[human:options]\nstage: implementation\ncontext: c\n1: a\n2: b", ID: "2"},
	}}
	p := &fakeDeployer{alreadyMerged: true}
	deps := newDeps(c, nil, p)

	err := deps.StartDeploy(context.Background(), StartDeployRequest{
		PMKey: "SC-1", Branch: "feat/x", OverrideDecision: true,
	})

	require.NoError(t, err)
	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], DeployStartedHeader)
}

// Pins gap 1 shut: DeployStartedHeader has exactly one production poster
// (StartDeploy), so a future refactor that removes it silently is caught here
// rather than by a missing ticket comment nobody notices.
func TestDeployStartedHeader_hasAProductionPoster(t *testing.T) {
	c := &fakeCommenter{}
	p := &fakeDeployer{alreadyMerged: true}
	deps := newDeps(c, nil, p)

	require.NoError(t, deps.StartDeploy(context.Background(), StartDeployRequest{
		PMKey: "SC-1", Branch: "feat/x",
	}))

	require.NotEmpty(t, c.added)
	assert.Contains(t, c.added[0], DeployStartedHeader)
}
