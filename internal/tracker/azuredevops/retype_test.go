package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// Azure DevOps chooses the work item type in the URL at create time, but it is
// an ordinary field afterwards — so a retype is a patch on System.WorkItemType
// carrying the same value create would have used (SC-3051).
func TestEditIssue_retypePatchesWorkItemType(t *testing.T) {
	for _, tc := range []struct {
		name     string
		asked    string
		wantWire string
	}{
		{name: "product work becomes a bug", asked: "Bug", wantWire: "Bug"},
		{name: "a spelling variant still reaches Azure's own marker", asked: "type:bug", wantWire: "Bug"},
		{name: "a bug becomes ordinary work", asked: "Task", wantWire: "Issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotOps []patchOp
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &gotOps))
				}
				_, _ = fmt.Fprintf(w, `{"id":5,"fields":{"System.Title":"Item","System.State":"New","System.WorkItemType":%q,"System.TeamProject":"Human"}}`, tc.wantWire)
			}))
			defer srv.Close()

			client := New(srv.URL, "myorg", "pat-test")
			issue, err := client.EditIssue(context.Background(), "Human/5", tracker.EditOptions{Type: &tc.asked})

			require.NoError(t, err)
			require.Len(t, gotOps, 1, "a retype is exactly one field patch")
			assert.Equal(t, "/fields/System.WorkItemType", gotOps[0].Path)
			assert.Equal(t, tc.wantWire, gotOps[0].Value)
			assert.Equal(t, tc.wantWire, issue.Type)
		})
	}
}

// A retype alone must not drag the tag set through a read-modify-write: tags
// are a different field and an untouched one must stay untouched.
func TestEditIssue_retypeLeavesTagsAlone(t *testing.T) {
	var gotOps []patchOp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Error("a retype must not fetch the work item for tag merging")
		}
		if r.Method == http.MethodPatch {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotOps))
		}
		_, _ = fmt.Fprint(w, `{"id":5,"fields":{"System.Title":"Item","System.State":"New","System.WorkItemType":"Bug","System.TeamProject":"Human","System.Tags":"alpha"}}`)
	}))
	defer srv.Close()

	bug := "Bug"
	client := New(srv.URL, "myorg", "pat-test")
	issue, err := client.EditIssue(context.Background(), "Human/5", tracker.EditOptions{Type: &bug})

	require.NoError(t, err)
	for _, op := range gotOps {
		assert.NotEqual(t, "/fields/System.Tags", op.Path)
	}
	assert.Equal(t, []string{"alpha"}, issue.Labels)
}
