package recall

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// countingProvider records how many per-issue fetches a sync performed.
func countingProvider(issues []tracker.Issue, detail map[string]tracker.Issue, gets *int) *mockProvider {
	return &mockProvider{
		listFn: func(context.Context, tracker.ListOptions) ([]tracker.Issue, error) { return issues, nil },
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			*gets++
			i := detail[key]
			return &i, nil
		},
	}
}

// A backend whose listing already carries the description must not be asked
// again for every issue: that turned a one-call sync into N+1 for nothing, and
// N is the whole ticket history (SC-2132).
func TestSync_ListWithDescriptionsSkipsThePerIssueFetch(t *testing.T) {
	gets := 0
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "one", Description: "already here"},
		{Key: "KAN-2", Title: "two", Description: "also here"},
	}
	s := newTestStore(t)

	res, err := Sync(context.Background(), s,
		[]tracker.Instance{{Name: "work", Kind: "shortcut", Provider: countingProvider(issues, nil, &gets)}},
		false, io.Discard)

	require.NoError(t, err)
	assert.Equal(t, 2, res.Indexed)
	assert.Zero(t, gets, "a listing that already carries descriptions needs no second call")
}

// A slim listing must still be filled in — that is why the per-issue fetch
// exists, and dropping it entirely would silently stop indexing descriptions on
// those backends.
func TestSync_SlimListStillFetchesEachIssue(t *testing.T) {
	gets := 0
	issues := []tracker.Issue{{Key: "KAN-1", Title: "one"}, {Key: "KAN-2", Title: "two"}}
	detail := map[string]tracker.Issue{
		"KAN-1": {Key: "KAN-1", Title: "one", Description: "fetched body"},
		"KAN-2": {Key: "KAN-2", Title: "two", Description: "other body"},
	}
	s := newTestStore(t)

	_, err := Sync(context.Background(), s,
		[]tracker.Instance{{Name: "work", Kind: "jira", Provider: countingProvider(issues, detail, &gets)}},
		false, io.Discard)
	require.NoError(t, err)

	assert.Equal(t, 2, gets, "a slim listing must be filled in per issue")
	found, err := s.Search(context.Background(), "fetched", 10)
	require.NoError(t, err)
	require.Len(t, found, 1, "the fetched description must reach the full-text index")
	assert.Equal(t, "KAN-1", found[0].Key)
}

// Mixed backends decide per issue, not per backend: one slim record among
// complete ones is filled in, and only that one.
func TestSync_FetchesOnlyTheIssuesMissingADescription(t *testing.T) {
	gets := 0
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "one", Description: "already here"},
		{Key: "KAN-2", Title: "two"},
	}
	detail := map[string]tracker.Issue{"KAN-2": {Key: "KAN-2", Title: "two", Description: "fetched body"}}
	s := newTestStore(t)

	_, err := Sync(context.Background(), s,
		[]tracker.Instance{{Name: "work", Kind: "jira", Provider: countingProvider(issues, detail, &gets)}},
		false, io.Discard)

	require.NoError(t, err)
	assert.Equal(t, 1, gets, "only the issue missing a description is re-fetched")
}
