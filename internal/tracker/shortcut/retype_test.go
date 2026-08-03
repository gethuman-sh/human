package shortcut

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

// Shortcut names the kind natively, so a retype is one field on the story
// update and must reach the wire as Shortcut's own vocabulary (SC-3051).
func TestEditIssue_retypeWritesStoryType(t *testing.T) {
	for _, tc := range []struct {
		name     string
		asked    string
		wantWire string
	}{
		{name: "product work becomes a bug", asked: "Bug", wantWire: "bug"},
		{name: "a bug becomes product work", asked: "Feature", wantWire: "feature"},
		{name: "chore is Shortcut's own vocabulary", asked: "chore", wantWire: "chore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPut && r.URL.Path == "/api/v3/stories/123":
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &gotBody))
					_, _ = fmt.Fprintf(w, `{"id":123,"name":"Story","story_type":%q,"workflow_state_id":500,"owner_ids":[],"requested_by_id":""}`, tc.wantWire)
				case r.URL.Path == "/api/v3/workflows":
					_, _ = fmt.Fprint(w, `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			client := New(srv.URL, "tok-test")
			issue, err := client.EditIssue(context.Background(), "123", tracker.EditOptions{Type: &tc.asked})

			require.NoError(t, err)
			assert.Equal(t, tc.wantWire, gotBody["story_type"], "PUT body must carry the new story type")
			assert.Equal(t, tc.wantWire, issue.Type)
			assert.NotContains(t, gotBody, "labels", "a retype must not rewrite the label set")
		})
	}
}

// A type Shortcut cannot express is refused rather than dropped: reporting a
// successful edit that left the story a bug is the exact failure a retype
// exists to end.
func TestEditIssue_retypeRefusesAnUnknownType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request must be made for a type Shortcut cannot express: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	epic := "Epic"
	client := New(srv.URL, "tok-test")
	_, err := client.EditIssue(context.Background(), "123", tracker.EditOptions{Type: &epic})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot express this issue type")
}
