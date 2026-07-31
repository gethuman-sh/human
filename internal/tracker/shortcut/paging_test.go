package shortcut

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

// workflowsJSON is the state map every list path resolves against.
const workflowsJSON = `[{"id":1,"name":"Default","states":[{"id":500,"name":"To Do","type":"unstarted"}]}]`

// Both response shapes must decode. Guessing one is silent: a bare array read as
// an envelope yields zero stories, which reads as an empty backlog rather than
// as a parse failure.
func TestDecodeStoryPage_AcceptsBothShapes(t *testing.T) {
	bare, err := decodeStoryPage([]byte(`[{"id":1,"name":"one"}]`))
	require.NoError(t, err)
	require.Len(t, bare.Stories, 1)
	assert.False(t, bare.Enveloped)
	assert.Empty(t, bare.Next)

	env, err := decodeStoryPage([]byte(`{"data":[{"id":2,"name":"two"}],"next":"/api/v3/x?token=abc"}`))
	require.NoError(t, err)
	require.Len(t, env.Stories, 1)
	assert.True(t, env.Enveloped)
	assert.Equal(t, "/api/v3/x?token=abc", env.Next)
}

func TestNextPathAndQuery(t *testing.T) {
	path, q, ok := nextPathAndQuery("/api/v3/stories/search?token=abc&page=2")
	require.True(t, ok)
	assert.Equal(t, "/api/v3/stories/search", path)
	assert.Equal(t, "token=abc&page=2", q)

	_, _, ok = nextPathAndQuery("")
	assert.False(t, ok, "an empty cursor is the end, not a page to fetch")
}

// A cursored response must be followed to the end, or "all past tickets" means
// "the first page" — and the prune then deletes everything beyond it.
func TestListIssuesPage_FollowsTheCursorToTheEnd(t *testing.T) {
	var searchCalls, pageCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/workflows":
			_, _ = fmt.Fprint(w, workflowsJSON)
		case r.URL.Path == "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"g1","name":"Human"}]`)
		case r.URL.Path == "/api/v3/groups/g1/stories" && r.URL.RawQuery == "":
			searchCalls++
			_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"one","workflow_state_id":500}],"next":"/api/v3/groups/g1/stories?token=p2"}`)
		case r.URL.Path == "/api/v3/groups/g1/stories":
			pageCalls++
			_, _ = fmt.Fprint(w, `{"data":[{"id":2,"name":"two","workflow_state_id":500}],"next":""}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	page, err := New(srv.URL, "tok").ListIssuesPage(context.Background(),
		tracker.ListOptions{Project: "Human", IncludeAll: true})

	require.NoError(t, err)
	assert.Equal(t, 1, searchCalls)
	assert.Equal(t, 1, pageCalls, "the cursor must be followed")
	require.Len(t, page.Issues, 2, "both pages must be collected")
	assert.False(t, page.Truncated, "a cursor followed to its end is complete")
}

// The honesty rule. A bare array carries no cursor, and that silence is NOT
// proof the server returned everything — it may have capped the response
// without saying so. Reporting Truncated=false here is the truthful answer
// (nothing was observed), which is precisely why the index cannot rely on this
// signal alone and keeps its own blast-radius guard (SC-2132).
func TestListIssuesPage_BareArrayReportsNoObservedTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, workflowsJSON)
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"g1","name":"Human"}]`)
		case "/api/v3/groups/g1/stories":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"one","workflow_state_id":500}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	page, err := New(srv.URL, "tok").ListIssuesPage(context.Background(),
		tracker.ListOptions{Project: "Human", IncludeAll: true})

	require.NoError(t, err)
	require.Len(t, page.Issues, 1)
	assert.False(t, page.Truncated)
}

// A cursor that cannot be followed leaves the result incomplete, and the caller
// must be told — silently returning a partial list is what makes a prune delete
// live work.
func TestListIssuesPage_UnfollowableCursorReportsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, workflowsJSON)
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"g1","name":"Human"}]`)
		case "/api/v3/groups/g1/stories":
			// A cursor the client cannot resolve into a request.
			_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"one","workflow_state_id":500}],"next":"::not a url::"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	page, err := New(srv.URL, "tok").ListIssuesPage(context.Background(),
		tracker.ListOptions{Project: "Human", IncludeAll: true})

	require.NoError(t, err)
	assert.Len(t, page.Issues, 1, "what was fetched is still returned")
	assert.True(t, page.Truncated, "an unfollowable cursor means there is more")
}

// A page fetch that fails mid-walk keeps what was collected and reports the gap,
// rather than losing the pages already in hand.
func TestListIssuesPage_FailedPageKeepsWhatItHasAndSaysItIsShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/workflows":
			_, _ = fmt.Fprint(w, workflowsJSON)
		case r.URL.Path == "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"g1","name":"Human"}]`)
		case r.URL.Path == "/api/v3/groups/g1/stories" && r.URL.RawQuery == "":
			_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"one","workflow_state_id":500}],"next":"/api/v3/groups/g1/stories?token=p2"}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	page, err := New(srv.URL, "tok").ListIssuesPage(context.Background(),
		tracker.ListOptions{Project: "Human", IncludeAll: true})

	require.NoError(t, err)
	assert.Len(t, page.Issues, 1)
	assert.True(t, page.Truncated)
}

// ListIssues keeps its contract for every existing caller: the same issues, with
// the page information dropped.
func TestListIssues_StillReturnsIssuesAfterPagingRefactor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/workflows":
			_, _ = fmt.Fprint(w, workflowsJSON)
		case "/api/v3/groups":
			_, _ = fmt.Fprint(w, `[{"id":"g1","name":"Human"}]`)
		case "/api/v3/groups/g1/stories":
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"one","workflow_state_id":500}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	issues, err := New(srv.URL, "tok").ListIssues(context.Background(), tracker.ListOptions{Project: "Human"})

	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "SC-1", issues[0].Key)
}
