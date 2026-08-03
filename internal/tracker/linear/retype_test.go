package linear

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// Linear has no issue type: the kind is a label, so a retype must arrive as a
// change to the issue's label set (SC-3051). Linear's labelIds is
// full-replacement, so the assertion is on the final set, not on a delta.
func TestEditIssue_retypeRemovesTheKindLabel(t *testing.T) {
	var gotLabelIDs []any
	seenIssueQuery := 0
	srv := httptest.NewServer(&graphQLHandler{
		t: t,
		handlers: map[string]func(vars map[string]any) string{
			"issueUpdate": func(vars map[string]any) string {
				input := vars["input"].(map[string]any)
				gotLabelIDs, _ = input["labelIds"].([]any)
				assert.NotContains(t, input, "title", "a retype must not touch the title")
				return `{"data":{"issueUpdate":{"success":true}}}`
			},
			"issue(": func(_ map[string]any) string {
				seenIssueQuery++
				// First: RetypeIntoLabels reads the live labels. Second: the
				// label-context lookup. Last: the post-edit re-read.
				if seenIssueQuery == 2 {
					return `{"data":{"issue":{"id":"uuid-1","team":{"id":"team-1"},"labels":{"nodes":[
						{"id":"lbl-bug","name":"bug"},{"id":"lbl-be","name":"backend"}]}}}}`
				}
				return `{"data":{"issue":{
					"identifier":"ENG-42","title":"T","description":"",
					"state":{"name":"Todo","type":"unstarted"},"priorityLabel":"",
					"assignee":null,"creator":null,
					"labels":{"nodes":[{"id":"lbl-bug","name":"bug"},{"id":"lbl-be","name":"backend"}]}
				}}}`
			},
		},
	})
	defer srv.Close()

	feature := "Feature"
	client := New(srv.URL, "lin_test")
	_, err := client.EditIssue(context.Background(), "ENG-42", tracker.EditOptions{Type: &feature})

	require.NoError(t, err)
	assert.Equal(t, []any{"lbl-be"}, gotLabelIDs, "the kind label goes, every other label stays")
}
