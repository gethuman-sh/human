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

// threadWithFixBetween builds a comment thread with a review round, a fixer
// run, and a second review round on top — the shape a continuing PR loop
// actually produces, as opposed to loopThread's reviews-only stack.
func threadWithFixBetween() []tracker.Comment {
	base := time.Unix(1000, 0)
	return []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", Created: base},
		{Body: "[human:pr-review-started]\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", Created: base.Add(1 * time.Minute)},
		{Body: "[human:pr-fix-started]\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", Created: base.Add(2 * time.Minute)},
		{Body: "[human:pr-review-started]\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", Created: base.Add(3 * time.Minute)},
	}
}

// The bug fixed in SC-5278: on the review exit of round 2, the newest
// pr-fix-started marker is still round 1's fixer, so round 1's fixer report
// reads as recorded+fresh (readStageReportSettled anchors only on the
// fixer's OWN started-marker). Attributing that report to round 2's just-
// recorded findings — which is what happens if the caller writes a
// disposition on exitRecorded+exitFresh alone — corrupts round 2's row with
// round 1's outcome before round 2's fixer has even run. fixDispositionIsFresh
// must refuse the write until the fix step is actually the newest loop step.
func TestRecordFixDisposition_notAttributedToWrongRoundOnReviewExit(t *testing.T) {
	store, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	logger := zerolog.Nop()
	comments := threadWithFixBetween()

	round1Findings := "BLOCKING a.go:10 — key logged raw — [security] the key is logged raw"
	round2Findings := "BLOCKING c.go:5 — unbounded loop — [perf] runs forever"

	// Round 1: reviewer finds an issue, fixer answers it.
	recordReviewRound(ctx, store, "proj", "SC-1", comments[:2], round1Findings, "abc123", logger)
	recordFixDisposition(ctx, store, "proj", "SC-1", comments[:3], "done", "fixed the leak", logger)

	// Round 2's review exit: the full thread now shows round 1's fixer as
	// the newest pr-fix-started marker (readPRFixReport re-reads round 1's
	// still-present report, which is recorded+fresh against its own
	// anchor). The guard must still refuse to attach it to round 2.
	recordReviewRound(ctx, store, "proj", "SC-1", comments, round2Findings, "def456", logger)
	assert.False(t, fixDispositionIsFresh(comments, true, true),
		"round 1's fixer is not the newest loop step once round 2's review has started")

	got, err := store.FindingsForKey(ctx, "proj", "SC-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "done", got[0].Disposition, "round 1's finding keeps its own disposition")
	assert.Equal(t, 1, got[0].Round)
	assert.Equal(t, "", got[1].Disposition, "round 2's finding carries no disposition until round 2's fixer reports")
	assert.Equal(t, 2, got[1].Round)

	// Round 2's fixer now runs and reports: the newest loop step is finally
	// PRStageFix, so the guard admits the write and it lands on round 2.
	fullThreadAfterFix := append(comments, tracker.Comment{
		Body: "[human:pr-fix-started]\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", Created: base1000Plus(4),
	})
	assert.True(t, fixDispositionIsFresh(fullThreadAfterFix, true, true))
	recordFixDisposition(ctx, store, "proj", "SC-1", fullThreadAfterFix, "done", "bounded the loop", logger)

	got, err = store.FindingsForKey(ctx, "proj", "SC-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "done", got[1].Disposition, "round 2's own fixer report attaches once it actually runs")
}

func base1000Plus(minutes int) time.Time {
	return time.Unix(1000, 0).Add(time.Duration(minutes) * time.Minute)
}

// The bug fixed in SC-5278: when the caller's ListComments failed it passes
// comments as nil, so PRLoopNumber/PRReviewRounds both read 0 off the empty
// thread. A verdict is only ever reached here because a real round's report
// settled against that round's own started-marker anchor, so round is never
// legitimately 0 — writing anyway would land the row at pr=0/round=0 and
// collide with (or misattribute into) another round's bucket under
// UNIQUE(project,key,pr,round,file,slug). recordReviewRound must refuse.
func TestRecordReviewRound_refusesWhenThreadUnread(t *testing.T) {
	store, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	logger := zerolog.Nop()

	recordReviewRound(ctx, store, "proj", "SC-1", nil, "BLOCKING a.go:1 — x — [tests] y", "h", logger)

	got, err := store.FindingsForKey(ctx, "proj", "SC-1")
	require.NoError(t, err)
	assert.Empty(t, got, "a nil comment thread must never produce a pr=0/round=0 row")
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
