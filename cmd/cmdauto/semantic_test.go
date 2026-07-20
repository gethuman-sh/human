package cmdauto

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// semanticStub implements tracker.Provider; only statuses, transitions, and
// edits carry behavior.
type semanticStub struct {
	statuses    []tracker.Status
	transitions []string
	editOpts    []tracker.EditOptions
	listErr     error
	transErr    error
	editErr     error
}

func (s *semanticStub) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *semanticStub) GetIssue(context.Context, string) (*tracker.Issue, error) { return nil, nil }
func (s *semanticStub) CreateIssue(context.Context, *tracker.Issue) (*tracker.Issue, error) {
	return nil, nil
}
func (s *semanticStub) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, nil
}
func (s *semanticStub) AddComment(context.Context, string, string) (*tracker.Comment, error) {
	return nil, nil
}
func (s *semanticStub) LinkIssues(context.Context, string, string) error { return nil }
func (s *semanticStub) DeleteIssue(context.Context, string) error        { return nil }
func (s *semanticStub) TransitionIssue(_ context.Context, _, status string) error {
	s.transitions = append(s.transitions, status)
	return s.transErr
}
func (s *semanticStub) AssignIssue(context.Context, string, string) error { return nil }
func (s *semanticStub) GetCurrentUser(context.Context) (string, error)    { return "", nil }
func (s *semanticStub) EditIssue(_ context.Context, _ string, opts tracker.EditOptions) (*tracker.Issue, error) {
	s.editOpts = append(s.editOpts, opts)
	return nil, s.editErr
}
func (s *semanticStub) ListStatuses(context.Context, string) ([]tracker.Status, error) {
	return s.statuses, s.listErr
}

func workflow() []tracker.Status {
	return []tracker.Status{
		{Name: "Backlog", Category: tracker.CategoryUnstarted},
		{Name: "In Progress", Category: tracker.CategoryStarted},
		{Name: "Done", Category: tracker.CategoryDone},
		{Name: "Cancelled", Category: tracker.CategoryClosed},
	}
}

func TestRunSemanticTransition_done(t *testing.T) {
	p := &semanticStub{statuses: workflow()}
	var buf bytes.Buffer

	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryDone, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"Done"}, p.transitions)
	assert.Contains(t, buf.String(), "Transitioned SC-1 to Done")
}

func TestRunSemanticTransition_closed(t *testing.T) {
	p := &semanticStub{statuses: workflow()}
	var buf bytes.Buffer

	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryClosed, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"Cancelled"}, p.transitions)
}

func TestRunSemanticTransition_closedFallsBackToDone(t *testing.T) {
	p := &semanticStub{statuses: []tracker.Status{
		{Name: "To Do", Category: tracker.CategoryUnstarted},
		{Name: "Done", Category: tracker.CategoryDone},
	}}
	var buf bytes.Buffer

	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryClosed, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"Done"}, p.transitions)
	assert.Contains(t, buf.String(), "No closed-type status")
}

func TestRunSemanticTransition_doneHasNoFallback(t *testing.T) {
	p := &semanticStub{statuses: []tracker.Status{
		{Name: "To Do", Category: tracker.CategoryUnstarted},
	}}
	var buf bytes.Buffer

	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryDone, false)
	require.Error(t, err)
	assert.Empty(t, p.transitions)
}

func TestRunSemanticTransition_firstOfCategoryWins(t *testing.T) {
	p := &semanticStub{statuses: []tracker.Status{
		{Name: "Shipped", Category: tracker.CategoryDone},
		{Name: "Archived", Category: tracker.CategoryDone},
	}}
	var buf bytes.Buffer

	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryDone, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"Shipped"}, p.transitions)
}

func TestRunSemanticTransition_listError(t *testing.T) {
	p := &semanticStub{listErr: errors.WithDetails("tracker down")}
	var buf bytes.Buffer
	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryDone, false)
	require.Error(t, err)
}

func TestRunSemanticTransition_transitionError(t *testing.T) {
	p := &semanticStub{statuses: workflow(), transErr: errors.WithDetails("forbidden")}
	var buf bytes.Buffer
	err := RunSemanticTransition(context.Background(), p, &buf, "SC-1", tracker.CategoryDone, false)
	require.Error(t, err)
}

func TestRunIdeaPromote_removesBothLabelSpellings(t *testing.T) {
	p := &semanticStub{}
	var buf bytes.Buffer

	err := RunIdeaPromote(context.Background(), p, &buf, "SC-1")
	require.NoError(t, err)
	require.Len(t, p.editOpts, 1)
	assert.Equal(t, []string{"human/idea", "idea"}, p.editOpts[0].RemoveLabels)
	assert.Contains(t, buf.String(), "Promoted SC-1")
}

func TestRunIdeaPromote_editError(t *testing.T) {
	p := &semanticStub{editErr: errors.WithDetails("forbidden")}
	var buf bytes.Buffer
	err := RunIdeaPromote(context.Background(), p, &buf, "SC-1")
	require.Error(t, err)
}
