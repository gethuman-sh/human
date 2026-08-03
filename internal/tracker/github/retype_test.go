package github

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

// GitHub has no issue type: the kind is a label, so a retype has to become a
// label swap that actually reaches the wire (SC-3051).
func TestEditIssue_retypeAddsTheKindLabel(t *testing.T) {
	var addBody map[string][]string
	var deletePaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octocat/repo/issues/1/labels":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &addBody))
			_, _ = fmt.Fprint(w, `[{"name":"backend"},{"name":"bug"}]`)
		case r.Method == http.MethodDelete:
			deletePaths = append(deletePaths, r.URL.EscapedPath())
			_, _ = fmt.Fprint(w, `[]`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octocat/repo/issues/1":
			_, _ = fmt.Fprint(w, `{"number":1,"title":"T","body":"d","state":"open","labels":[{"name":"backend"}]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	bug := "Bug"
	client := New(srv.URL, "ghp_test")
	_, err := client.EditIssue(context.Background(), "octocat/repo#1", tracker.EditOptions{Type: &bug})

	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"labels": {"bug"}}, addBody)
	assert.Empty(t, deletePaths, "nothing to remove: the issue carried no kind label")
}

// The other direction: retyping a defect back to product work has to take the
// kind label OFF, or the ticket keeps being picked up by the bug pipeline.
func TestEditIssue_retypeRemovesTheKindLabel(t *testing.T) {
	var deletePaths []string
	postCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			postCalled = true
			_, _ = fmt.Fprint(w, `[]`)
		case http.MethodDelete:
			deletePaths = append(deletePaths, r.URL.EscapedPath())
			_, _ = fmt.Fprint(w, `[]`)
		case http.MethodGet:
			// Non-canonical spelling on purpose: the kind is recognised by
			// token, so "kind/bug" must be the label that is removed.
			_, _ = fmt.Fprint(w, `{"number":1,"title":"T","body":"d","state":"open","labels":[{"name":"kind/bug"},{"name":"backend"}]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	feature := "Feature"
	client := New(srv.URL, "ghp_test")
	_, err := client.EditIssue(context.Background(), "octocat/repo#1", tracker.EditOptions{Type: &feature})

	require.NoError(t, err)
	assert.False(t, postCalled, "nothing to add when the new kind has no label of its own")
	assert.Equal(t, []string{"/repos/octocat/repo/issues/1/labels/kind/bug"}, deletePaths)
}
