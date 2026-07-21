package forge

import (
	"context"
	"testing"

	"github.com/gethuman-sh/human/errors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeForge scripts a Creator that is also a PullRequestFinder, so the
// adopt-vs-create decision in AdoptOrCreatePullRequest can be exercised without
// a real forge (SC-989).
type fakeForge struct {
	// findResults are returned by successive FindOpenPullRequest calls, so a
	// test can script "miss, then hit" for the 422 safety-net path.
	findResults []*PullRequest
	findErr     error
	findCalls   int

	created     *PullRequest
	createErr   error
	createCalls int
}

func (f *fakeForge) FindOpenPullRequest(_ context.Context, _, _ string) (*PullRequest, error) {
	idx := f.findCalls
	f.findCalls++
	if f.findErr != nil {
		return nil, f.findErr
	}
	if idx < len(f.findResults) {
		return f.findResults[idx], nil
	}
	return nil, nil
}

func (f *fakeForge) CreatePullRequest(_ context.Context, _ *PullRequest) (*PullRequest, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.created, nil
}

// creatorOnly is a Creator with no finder capability, proving the helper falls
// back to a plain create for providers that cannot look PRs up.
type creatorOnly struct {
	created     *PullRequest
	createCalls int
}

func (c *creatorOnly) CreatePullRequest(_ context.Context, _ *PullRequest) (*PullRequest, error) {
	c.createCalls++
	return c.created, nil
}

func TestAdoptOrCreatePullRequest_adoptsExistingOpenPR(t *testing.T) {
	f := &fakeForge{
		findResults: []*PullRequest{{Number: 42}},
		created:     &PullRequest{Number: 100},
	}
	got, err := AdoptOrCreatePullRequest(context.Background(), f, &PullRequest{Head: "autofix/989"})
	require.NoError(t, err)
	assert.Equal(t, 42, got.Number)
	assert.Equal(t, 0, f.createCalls, "must not create when an open PR already exists")
	assert.Equal(t, 1, f.findCalls)
}

func TestAdoptOrCreatePullRequest_createsWhenNoneExists(t *testing.T) {
	f := &fakeForge{
		findResults: nil, // finder misses
		created:     &PullRequest{Number: 100},
	}
	got, err := AdoptOrCreatePullRequest(context.Background(), f, &PullRequest{Head: "autofix/989"})
	require.NoError(t, err)
	assert.Equal(t, 100, got.Number)
	assert.Equal(t, 1, f.createCalls)
}

func TestAdoptOrCreatePullRequest_422SafetyNetReQueriesAndAdopts(t *testing.T) {
	f := &fakeForge{
		findResults: []*PullRequest{nil, {Number: 55}}, // miss, then hit on re-query
		createErr:   errors.WithDetails("create failed", "statusCode", 422),
	}
	got, err := AdoptOrCreatePullRequest(context.Background(), f, &PullRequest{Head: "autofix/989"})
	require.NoError(t, err)
	assert.Equal(t, 55, got.Number)
	assert.Equal(t, 1, f.createCalls)
	assert.Equal(t, 2, f.findCalls, "422 must trigger exactly one re-query")
}

func TestAdoptOrCreatePullRequest_noFinderFallsBackToCreate(t *testing.T) {
	c := &creatorOnly{created: &PullRequest{Number: 7}}
	got, err := AdoptOrCreatePullRequest(context.Background(), c, &PullRequest{Head: "autofix/989"})
	require.NoError(t, err)
	assert.Equal(t, 7, got.Number)
	assert.Equal(t, 1, c.createCalls)
}

func TestAdoptOrCreatePullRequest_createErrorNon422Propagates(t *testing.T) {
	f := &fakeForge{
		findResults: nil,
		createErr:   errors.WithDetails("boom", "statusCode", 500),
	}
	_, err := AdoptOrCreatePullRequest(context.Background(), f, &PullRequest{Head: "autofix/989"})
	require.Error(t, err)
	assert.Equal(t, 1, f.createCalls)
	assert.Equal(t, 1, f.findCalls, "a non-422 create error must not trigger a re-query")
}
