package recall

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewFindings_recordThenDispose(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	round1 := []ReviewFinding{
		{Project: "p", Key: "SC-1", PR: 7, Round: 1, Head: "aaa", File: "a.go", Slug: "log injection", Class: "security", Text: "user input reaches the log line"},
		{Project: "p", Key: "SC-1", PR: 7, Round: 1, Head: "aaa", File: "b.go", Slug: "caller untested", Class: "tests"},
	}
	require.NoError(t, s.RecordReviewFindings(ctx, round1))
	// A re-drive of the same round refreshes rather than duplicates.
	round1[0].Text = "user input still reaches the log line"
	require.NoError(t, s.RecordReviewFindings(ctx, round1[:1]))
	require.NoError(t, s.SetFindingDisposition(ctx, "p", "SC-1", 7, 1, "done", "escaped the input"))
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", PR: 7, Round: 2, Head: "bbb", File: "a.go", Slug: "log injection again", Class: "security"},
	}))

	got, err := s.FindingsForKey(ctx, "p", "SC-1")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "user input still reaches the log line", got[0].Text)
	assert.Equal(t, "done", got[0].Disposition)
	assert.Equal(t, "escaped the input", got[1].Note, "the disposition covers every finding of the round")
	assert.Empty(t, got[2].Disposition, "the next round's fixer has not reported")
	assert.False(t, got[0].RecordedAt.IsZero())

	counts, err := s.FindingClassCounts(ctx, "p", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"security": 2, "tests": 1}, counts)
	counts, err = s.FindingClassCounts(ctx, "other", time.Time{})
	require.NoError(t, err)
	assert.Empty(t, counts, "projects do not share findings")
}

func TestReviewFindings_emptyRoundWritesNothing(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.RecordReviewFindings(context.Background(), nil))
	got, err := s.FindingsForKey(context.Background(), "", "SC-1")
	require.NoError(t, err)
	assert.Empty(t, got)
}
