package recall

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// listing builds a provider that returns exactly these issues, whatever is asked.
func listing(issues ...tracker.Issue) *mockProvider {
	byKey := make(map[string]tracker.Issue, len(issues))
	for _, i := range issues {
		byKey[i.Key] = i
	}
	return &mockProvider{
		listFn: func(context.Context, tracker.ListOptions) ([]tracker.Issue, error) { return issues, nil },
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			i := byKey[key]
			return &i, nil
		},
	}
}

func instance(p *mockProvider) []tracker.Instance {
	return []tracker.Instance{{Name: "work", Kind: "jira", Provider: p}}
}

// The safety property the unattended scheduler depends on: a delta pass must
// never delete anything.
//
// recall.Sync prunes every key it did not see, and a listing that came back
// short — because the backend capped it, not because the work is gone — would
// take the rest of the record with it. A delta pass must therefore leave
// tickets it did not fetch this time completely alone (SC-2132).
func TestSync_DeltaPassNeverPrunes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A first pass establishes two tickets and the per-source watermark.
	_, err := Sync(ctx, s, instance(listing(
		tracker.Issue{Key: "KAN-1", Title: "first"},
		tracker.Issue{Key: "KAN-2", Title: "second"},
	)), false, io.Discard)
	require.NoError(t, err)

	// A later delta returns only what changed — the other ticket is untouched
	// upstream, not deleted.
	res, err := Sync(ctx, s, instance(listing(tracker.Issue{Key: "KAN-1", Title: "first, edited"})), false, io.Discard)
	require.NoError(t, err)

	assert.Zero(t, res.Pruned, "a delta pass must never prune")
	keys, err := s.AllKeys(ctx, "work")
	require.NoError(t, err)
	assert.Contains(t, keys, "KAN-2", "a ticket absent from a delta listing must survive")
	assert.Contains(t, keys, "KAN-1")
}

// The first pass against an empty source has no watermark, so it is not
// incremental and the prune runs — it must be harmless because there is
// nothing yet to delete.
func TestSync_FirstPassPrunesNothing(t *testing.T) {
	s := newTestStore(t)

	res, err := Sync(context.Background(), s, instance(listing(tracker.Issue{Key: "KAN-1", Title: "first"})), false, io.Discard)

	require.NoError(t, err)
	assert.Zero(t, res.Pruned)
	assert.Equal(t, 1, res.Indexed)
}

// A delta pass carries the ticket's own state, which is what makes a past
// ticket meaningful in the record.
func TestSync_CarriesTrackerStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := Sync(ctx, s, instance(listing(
		tracker.Issue{Key: "KAN-1", Title: "shipped thing", Status: "Done"},
	)), false, io.Discard)
	require.NoError(t, err)

	found, err := s.Search(ctx, "shipped", 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "Done", found[0].Status, "the tracker's own status is the ticket's state")
}
