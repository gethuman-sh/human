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

// A directional link must be written with the directional verb. Recording it as
// the symmetric one would gate nothing while appearing to, and the caller could
// not tell the difference.
func TestLinkIssues_BlocksSendsTheDirectionalVerb(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/story-links" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			_, _ = fmt.Fprint(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	require.NoError(t, New(srv.URL, "tok").LinkIssues(context.Background(), "SC-100", "SC-200", tracker.LinkBlocks))

	assert.Equal(t, "blocks", body["verb"])
	assert.EqualValues(t, 100, body["subject_id"], "the subject is the one that must finish first")
	assert.EqualValues(t, 200, body["object_id"])
}

// The existing symmetric behaviour is unchanged for callers that do not ask for
// a dependency.
func TestLinkIssues_RelatedKeepsTheSymmetricVerb(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	require.NoError(t, New(srv.URL, "tok").LinkIssues(context.Background(), "SC-1", "SC-2", tracker.LinkRelated))

	assert.Equal(t, "relates to", body["verb"])
}

// A kind we cannot express is refused rather than downgraded.
func TestLinkIssues_UnknownKindIsRefused(t *testing.T) {
	err := New("http://unused", "tok").LinkIssues(context.Background(), "SC-1", "SC-2", tracker.LinkKind("supersedes"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot express")
}

// Unlinking is how work held behind a mistaken or abandoned blocker is
// released, so it must find the record whichever end it was written from — the
// caller should not have to know which story was the subject.
func TestUnlinkIssues_RemovesTheLinkFromEitherDirection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		links   string
		wantDel string
	}{
		{"this story is the subject", `[{"id":7,"verb":"blocks","subject_id":100,"object_id":200}]`, "/api/v3/story-links/7"},
		{"this story is the object", `[{"id":9,"verb":"blocks","subject_id":200,"object_id":100}]`, "/api/v3/story-links/9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deleted string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v3/stories/100":
					_, _ = fmt.Fprintf(w, `{"id":100,"name":"one","workflow_state_id":500,"story_links":%s}`, tc.links)
				case r.Method == http.MethodDelete:
					deleted = r.URL.Path
					_, _ = fmt.Fprint(w, `{}`)
				case r.URL.Path == "/api/v3/workflows":
					_, _ = fmt.Fprint(w, `[{"id":1,"name":"D","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			require.NoError(t, New(srv.URL, "tok").UnlinkIssues(context.Background(), "SC-100", "SC-200"))
			assert.Equal(t, tc.wantDel, deleted)
		})
	}
}

// Unlinking two stories that are not linked removes nothing and is not an
// error: the caller asked for a state that already holds.
func TestUnlinkIssues_UnrelatedPairDeletesNothing(t *testing.T) {
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/stories/100":
			_, _ = fmt.Fprint(w, `{"id":100,"name":"one","story_links":[{"id":7,"verb":"blocks","subject_id":100,"object_id":999}]}`)
		case r.Method == http.MethodDelete:
			deleted = true
			_, _ = fmt.Fprint(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	require.NoError(t, New(srv.URL, "tok").UnlinkIssues(context.Background(), "SC-100", "SC-200"))
	assert.False(t, deleted, "an unrelated link must not be removed")
}
