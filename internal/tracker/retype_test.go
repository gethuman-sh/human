package tracker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetypeLabels(t *testing.T) {
	tests := []struct {
		name       string
		current    []string
		newType    string
		wantAdd    []string
		wantRemove []string
	}{
		{
			name:    "product work becomes a bug",
			current: []string{"backend"},
			newType: "Bug",
			wantAdd: []string{"bug"},
		},
		{
			name:       "a bug becomes product work",
			current:    []string{"backend", "bug"},
			newType:    "Feature",
			wantRemove: []string{"bug"},
		},
		{
			// The kind is recognised by token, so a ticket labelled "kind/bug"
			// must lose THAT label — removing only the canonical "bug" would
			// leave it reading as a bug after a retype that reported success.
			name:       "a non-canonical kind label is removed too",
			current:    []string{"kind/bug", "backend"},
			newType:    "Chore",
			wantRemove: []string{"kind/bug"},
		},
		{
			name:       "bug becomes security: one kind replaces the other",
			current:    []string{"bug"},
			newType:    "Security",
			wantAdd:    []string{"security"},
			wantRemove: []string{"bug"},
		},
		{
			// Already the requested kind: nothing to do, and in particular the
			// label is not added a second time.
			name:    "retyping to the kind it already is changes nothing",
			current: []string{"type:bug", "backend"},
			newType: "Bug",
		},
		{
			name:    "no labels at all",
			current: nil,
			newType: "Bug",
			wantAdd: []string{"bug"},
		},
		{
			// Labels that are not kind markers are never touched, whichever
			// direction the retype goes.
			name:       "unrelated labels survive",
			current:    []string{"p1", "bug", "customer/acme"},
			newType:    "Task",
			wantRemove: []string{"bug"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			add, remove := RetypeLabels(tc.current, tc.newType)
			assert.Equal(t, tc.wantAdd, add, "labels to add")
			assert.Equal(t, tc.wantRemove, remove, "labels to remove")
		})
	}
}

// retypeGetter is a Getter that hands back one fixed issue and counts reads, so
// the tests can assert the extra fetch happens only for an actual retype.
type retypeGetter struct {
	issue *Issue
	err   error
	calls int
}

func (g *retypeGetter) GetIssue(context.Context, string) (*Issue, error) {
	g.calls++
	return g.issue, g.err
}

func TestRetypeIntoLabels_foldsTheKindIntoLabelEdits(t *testing.T) {
	g := &retypeGetter{issue: &Issue{Labels: []string{"backend", "bug"}}}
	feature := "Feature"

	got, err := RetypeIntoLabels(context.Background(), g, "KEY-1", EditOptions{
		Type:      &feature,
		AddLabels: []string{"already-asked-for"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"already-asked-for"}, got.AddLabels, "the caller's own label edits survive")
	assert.Equal(t, []string{"bug"}, got.RemoveLabels)
	assert.Nil(t, got.Type, "Type is cleared once it has become label work")
	assert.Equal(t, 1, g.calls)
}

func TestRetypeIntoLabels_noTypeAskedForReadsNothing(t *testing.T) {
	g := &retypeGetter{issue: &Issue{Labels: []string{"bug"}}}
	title := "New title"

	got, err := RetypeIntoLabels(context.Background(), g, "KEY-1", EditOptions{Title: &title})

	require.NoError(t, err)
	assert.Equal(t, &title, got.Title)
	assert.Empty(t, got.AddLabels)
	assert.Empty(t, got.RemoveLabels)
	assert.Zero(t, g.calls, "an edit that does not retype must not pay for a read")
}

func TestRetypeIntoLabels_unreadableIssueFailsTheEdit(t *testing.T) {
	g := &retypeGetter{err: assert.AnError}
	bug := "Bug"

	_, err := RetypeIntoLabels(context.Background(), g, "KEY-1", EditOptions{Type: &bug})

	// Proceeding blind would write a label swap computed from no labels, which
	// can silently leave the old kind in place — the one thing a retype must
	// never do. Failing is the honest answer.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading current labels to retype the issue")
}
