package gitlab

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

// GitLab has no issue type: the kind is a label, so a retype becomes the same
// atomic add/remove a label swap uses (SC-3051).
func TestEditIssue_retypeSwapsTheKindLabel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		live       string
		asked      string
		wantAdd    any
		wantRemove any
	}{
		{
			name:    "product work becomes a bug",
			live:    `["backend"]`,
			asked:   "Bug",
			wantAdd: "bug",
		},
		{
			name:       "a bug becomes product work",
			live:       `["backend","bug"]`,
			asked:      "Feature",
			wantRemove: "bug",
		},
		{
			name:       "a non-canonical kind label is the one removed",
			live:       `["type:bug"]`,
			asked:      "Task",
			wantRemove: "type:bug",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_, _ = fmt.Fprintf(w, `{"iid":1,"title":"T","state":"opened","labels":%s}`, tc.live)
				case http.MethodPut:
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &got))
					_, _ = fmt.Fprint(w, `{"iid":1,"title":"T","state":"opened","labels":[]}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()

			client := New(srv.URL, "glpat-test")
			_, err := client.EditIssue(context.Background(), "group/proj#1", tracker.EditOptions{Type: &tc.asked})

			require.NoError(t, err)
			assert.Equal(t, tc.wantAdd, got["add_labels"])
			assert.Equal(t, tc.wantRemove, got["remove_labels"])
			assert.NotContains(t, got, "title", "a retype must not touch the title")
		})
	}
}
