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

func TestFindingsForFiles_newestFirstWithDisposition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", PR: 7, Round: 1, File: "a.go", Slug: "log injection", Class: "security"},
	}))
	require.NoError(t, s.SetFindingDisposition(ctx, "p", "SC-1", 7, 1, "done", "escaped the input"))
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", PR: 7, Round: 2, File: "a.go", Slug: "caller untested", Class: "tests"},
	}))

	got, err := s.FindingsForFiles(ctx, "p", []string{"a.go"}, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 2, got[0].Round, "newest round first")
	assert.Equal(t, "done", got[1].Disposition)
	assert.Equal(t, "escaped the input", got[1].Note)
}

func TestFindingsForFiles_projectsDoNotShare(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", File: "a.go", Slug: "x"},
	}))

	got, err := s.FindingsForFiles(ctx, "other", []string{"a.go"}, 0)
	require.NoError(t, err)
	assert.Empty(t, got, "a miss is an empty answer, not an error")
}

func TestFindingsForFiles_suffixMatchesAbsoluteRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", File: "/work/repo/internal/a.go", Slug: "x"},
	}))

	got, err := s.FindingsForFiles(ctx, "p", []string{"internal/a.go"}, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/work/repo/internal/a.go", got[0].File)
}

func TestFindingsForFiles_suffixIsAnchored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", File: "internal/xa.go", Slug: "x"},
	}))

	got, err := s.FindingsForFiles(ctx, "p", []string{"a.go"}, 0)
	require.NoError(t, err)
	assert.Empty(t, got, "the suffix is anchored on a path separator")
}

func TestFindingsForFiles_underscoreIsLiteral(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", File: "internal/pr_loop.go", Slug: "x"},
		{Project: "p", Key: "SC-2", File: "internal/prxloop.go", Slug: "y"},
	}))

	got, err := s.FindingsForFiles(ctx, "p", []string{"pr_loop.go"}, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "internal/pr_loop.go", got[0].File)
}

func TestFindingsForFiles_emptyInputs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	got, err := s.FindingsForFiles(ctx, "p", nil, 0)
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = s.FindingsForFiles(ctx, "p", []string{"  "}, 0)
	require.NoError(t, err)
	assert.Nil(t, got, "a blank path runs no query at all")
}

func TestFindingsForFiles_limit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
			{Project: "p", Key: "SC-1", PR: 7, Round: i, File: "a.go", Slug: "round"},
		}))
	}

	got, err := s.FindingsForFiles(ctx, "p", []string{"a.go"}, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 5, got[0].Round)
	assert.Equal(t, 4, got[1].Round)
}

func TestFindingsForFiles_severalPathsAtOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewFindings(ctx, []ReviewFinding{
		{Project: "p", Key: "SC-1", File: "a.go", Slug: "x"},
		{Project: "p", Key: "SC-2", File: "b.go", Slug: "y"},
		{Project: "p", Key: "SC-3", File: "c.go", Slug: "z"},
	}))

	got, err := s.FindingsForFiles(ctx, "p", []string{"a.go", "c.go"}, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	files := []string{got[0].File, got[1].File}
	assert.ElementsMatch(t, []string{"a.go", "c.go"}, files)
}
