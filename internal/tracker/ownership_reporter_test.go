package tracker_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// reporterProvider can read an issue and hand it to its reporter — the two
// capabilities the guarded repair needs.
type reporterProvider struct {
	issue    *tracker.Issue
	getErr   error
	assigned []string
	assErr   error
}

func (r *reporterProvider) GetIssue(_ context.Context, _ string) (*tracker.Issue, error) {
	return r.issue, r.getErr
}

func (r *reporterProvider) AssignToReporter(_ context.Context, key string) error {
	if r.assErr != nil {
		return r.assErr
	}
	r.assigned = append(r.assigned, key)
	return nil
}

func TestAssignToReporterIfUnownedAssignsAnOwnerlessIssue(t *testing.T) {
	p := &reporterProvider{issue: &tracker.Issue{Key: "SC-1", Reporter: "André Neubauer"}}

	assigned, err := tracker.AssignToReporterIfUnowned(context.Background(), p, "SC-1")

	require.NoError(t, err)
	assert.True(t, assigned)
	assert.Equal(t, []string{"SC-1"}, p.assigned)
}

// A repair that overwrites a deliberate assignment is not a repair.
func TestAssignToReporterIfUnownedLeavesAnOwnedIssueAlone(t *testing.T) {
	p := &reporterProvider{issue: &tracker.Issue{Key: "SC-1", Assignee: "Someone Else", Reporter: "André Neubauer"}}

	assigned, err := tracker.AssignToReporterIfUnowned(context.Background(), p, "SC-1")

	require.NoError(t, err)
	assert.False(t, assigned)
	assert.Empty(t, p.assigned, "an owned issue is never reassigned")
}

// Whitespace is not an owner: a field holding only spaces must still repair.
func TestAssignToReporterIfUnownedTreatsBlankAssigneeAsUnowned(t *testing.T) {
	p := &reporterProvider{issue: &tracker.Issue{Key: "SC-1", Assignee: "   "}}

	assigned, err := tracker.AssignToReporterIfUnowned(context.Background(), p, "SC-1")

	require.NoError(t, err)
	assert.True(t, assigned)
}

func TestAssignToReporterIfUnownedReportsUnsupportedAndFailures(t *testing.T) {
	_, err := tracker.AssignToReporterIfUnowned(context.Background(), &assignOnlyProvider{}, "SC-1")
	require.ErrorIs(t, err, tracker.ErrOwnershipUnsupported)

	boom := stderrors.New("boom")
	_, err = tracker.AssignToReporterIfUnowned(context.Background(), &reporterProvider{getErr: boom}, "SC-1")
	require.ErrorIs(t, err, boom, "a read failure must not be mistaken for 'already owned'")

	_, err = tracker.AssignToReporterIfUnowned(context.Background(),
		&reporterProvider{issue: &tracker.Issue{Key: "SC-1"}, assErr: boom}, "SC-1")
	require.ErrorIs(t, err, boom)
}
