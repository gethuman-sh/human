package cmddaemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/tracker"
)

func loopThread(rounds int) []tracker.Comment {
	base := time.Unix(1000, 0)
	out := []tracker.Comment{{Body: "[human:ready-for-review]\nbranch: feat/x", Created: base}}
	for i := 0; i < rounds; i++ {
		out = append(out, tracker.Comment{Body: "[human:pr-review-started]\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", Created: base.Add(time.Duration(i+1) * time.Minute)})
	}
	return out
}

// The record half of SC-5278: a round's findings land in the recall index
// keyed by project, ticket, PR and round, and the fixer's exit is attached to
// that round when it reports. A re-drive of the same round writes nothing new.
func TestRecordReviewRound_writesTheRoundAndItsDisposition(t *testing.T) {
	store, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	logger := zerolog.Nop()
	findings := "BLOCKING a.go:10 — key logged raw — [security] the key is logged raw\nBLOCKING b.go:1 — caller untested — [tests] no test"

	recordReviewRound(ctx, store, "proj", "SC-1", loopThread(2), findings, "abc123", logger)
	recordReviewRound(ctx, store, "proj", "SC-1", loopThread(2), findings, "abc123", logger)
	recordFixDisposition(ctx, store, "proj", "SC-1", loopThread(2), "done", "escaped the key", logger)

	got, err := store.FindingsForKey(ctx, "proj", "SC-1")
	require.NoError(t, err)
	require.Len(t, got, 2, "a re-drive refreshes the round rather than duplicating it")
	assert.Equal(t, recall.ReviewFinding{Project: "proj", Key: "SC-1", PR: 7, Round: 2, Head: "abc123",
		File: "a.go", Slug: "key logged raw", Class: "security", Text: "the key is logged raw",
		Disposition: "done", Note: "escaped the key", RecordedAt: got[0].RecordedAt}, got[0])
	assert.Equal(t, "tests", got[1].Class)

	counts, err := store.FindingClassCounts(ctx, "proj", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"security": 1, "tests": 1}, counts)
}

// No recorder, no findings, no exit: nothing is written and nothing panics.
func TestRecordReviewRound_toleratesNothingToRecord(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.Nop()
	recordReviewRound(ctx, nil, "", "SC-1", loopThread(1), "BLOCKING a.go:1 — x — [tests] y", "h", logger)
	recordFixDisposition(ctx, nil, "", "SC-1", loopThread(1), "done", "", logger)

	store, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	recordReviewRound(ctx, store, "", "SC-1", loopThread(1), "no blocking issues", "h", logger)
	recordFixDisposition(ctx, store, "", "SC-1", loopThread(1), "", "", logger)
	got, err := store.FindingsForKey(ctx, "", "SC-1")
	require.NoError(t, err)
	assert.Empty(t, got)
}
