package clickup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// ClickUp has no task type: the kind is a tag, so a retype becomes the same tag
// add/remove an ordinary label edit uses (SC-3051).
func TestEditIssue_retypeSwapsTheKindTag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		live     string
		asked    string
		wantCall string
	}{
		{
			name:     "product work becomes a bug",
			live:     `[{"name":"backend"}]`,
			asked:    "Bug",
			wantCall: "POST /api/v2/task/abc123/tag/bug",
		},
		{
			name:     "a bug becomes product work",
			live:     `[{"name":"bug"}]`,
			asked:    "Feature",
			wantCall: "DELETE /api/v2/task/abc123/tag/bug",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.Method == http.MethodGet {
					_, _ = fmt.Fprintf(w, `{"id":"abc123","name":"T","status":{"status":"open","type":"open"},"list":{"id":"901"},"tags":%s}`, tc.live)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			client := New(srv.URL, "tok-test", "")
			_, err := client.EditIssue(context.Background(), "abc123", tracker.EditOptions{Type: &tc.asked})

			require.NoError(t, err)
			assert.Contains(t, calls, tc.wantCall)
			for _, c := range calls {
				assert.NotEqual(t, "PUT /api/v2/task/abc123", c, "a retype alone must not PUT the task's fields")
			}
		})
	}
}
