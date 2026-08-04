package tracker_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// ownerProvider implements both halves of ownership: resolving the caller's
// identity and assigning an issue to it.
type ownerProvider struct {
	userID     string
	userErr    error
	assignErr  error
	assignedTo string
	assignKey  string
	calls      int
}

func (o *ownerProvider) GetCurrentUser(context.Context) (string, error) {
	return o.userID, o.userErr
}

func (o *ownerProvider) AssignIssue(_ context.Context, key, userID string) error {
	o.calls++
	if o.assignErr != nil {
		return o.assignErr
	}
	o.assignKey = key
	o.assignedTo = userID
	return nil
}

// assignOnlyProvider can assign but cannot say who the caller is — the shape a
// backend takes when it has an assignee field but no "current user" endpoint.
type assignOnlyProvider struct{ calls int }

func (a *assignOnlyProvider) AssignIssue(context.Context, string, string) error {
	a.calls++
	return nil
}

func TestAssignToCurrentUserAssignsTheAuthenticatedIdentity(t *testing.T) {
	p := &ownerProvider{userID: "member-42"}

	require.NoError(t, tracker.AssignToCurrentUser(context.Background(), p, "SC-1"))

	assert.Equal(t, "SC-1", p.assignKey)
	assert.Equal(t, "member-42", p.assignedTo, "the owner must be the identity the credential belongs to")
}

func TestAssignToCurrentUserReportsUnsupportedWithoutBothCapabilities(t *testing.T) {
	only := &assignOnlyProvider{}

	err := tracker.AssignToCurrentUser(context.Background(), only, "SC-1")

	require.ErrorIs(t, err, tracker.ErrOwnershipUnsupported)
	assert.Zero(t, only.calls, "a provider that cannot name the current user must not be asked to assign")
}

// An empty user ID would assign the ticket to nobody, silently clearing an
// existing owner — worse than declining to claim it.
func TestAssignToCurrentUserRefusesAnEmptyIdentity(t *testing.T) {
	p := &ownerProvider{userID: ""}

	err := tracker.AssignToCurrentUser(context.Background(), p, "SC-1")

	require.Error(t, err)
	assert.NotErrorIs(t, err, tracker.ErrOwnershipUnsupported)
	assert.Zero(t, p.calls)
}

func TestAssignToCurrentUserPropagatesIdentityAndAssignFailures(t *testing.T) {
	boom := stderrors.New("boom")

	err := tracker.AssignToCurrentUser(context.Background(), &ownerProvider{userErr: boom}, "SC-1")
	require.ErrorIs(t, err, boom)

	err = tracker.AssignToCurrentUser(context.Background(), &ownerProvider{userID: "u1", assignErr: boom}, "SC-1")
	require.ErrorIs(t, err, boom)
}

// Best-effort exists so ownership never decides whether real work counts as
// having happened.
func TestAssignToCurrentUserBestEffortSwallowsUnsupportedButReportsRealFailures(t *testing.T) {
	ok, err := tracker.AssignToCurrentUserBestEffort(context.Background(), &assignOnlyProvider{}, "SC-1")
	assert.False(t, ok)
	require.NoError(t, err, "a backend without an assignee concept is nothing to report")

	boom := stderrors.New("boom")
	ok, err = tracker.AssignToCurrentUserBestEffort(context.Background(), &ownerProvider{userID: "u1", assignErr: boom}, "SC-1")
	assert.False(t, ok)
	require.ErrorIs(t, err, boom, "a genuine failure stays available to whoever wants to log it")

	p := &ownerProvider{userID: "u1"}
	ok, err = tracker.AssignToCurrentUserBestEffort(context.Background(), p, "SC-1")
	assert.True(t, ok)
	require.NoError(t, err)
}

func TestAssignToCurrentUserBestEffortIgnoresAnEmptyKey(t *testing.T) {
	p := &ownerProvider{userID: "u1"}

	ok, err := tracker.AssignToCurrentUserBestEffort(context.Background(), p, "")

	assert.False(t, ok)
	require.NoError(t, err)
	assert.Zero(t, p.calls, "there is no ticket to claim")
}
