package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A stage marked failed and then finishing later, with no relaunch in
// between, is the reaper's own contradiction — the run was alive the whole
// time (SC-3853, acceptance criterion 2).
func TestLateResultCandidates_FailedThenSuccessWithNoRelaunchIsACandidate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(ReviewFailedHeader, t0),
		cmt(ReviewCompleteHeader, t1),
	}
	got := lateResultCandidates(comments)
	require.Len(t, got, 1)
	assert.Equal(t, "review", got[0].Pair)
	assert.Equal(t, BoardVerification, got[0].Stage)
	assert.Equal(t, t0, got[0].FailedAt)
	assert.Equal(t, t1, got[0].SuccessAt)
}

// A failure followed by a genuine relaunch, and only then a success, is the
// machine working as designed — the retry succeeded. It must never count
// (acceptance criterion 3: no retry budget spent on a run that was alive the
// whole time reads the other way too — a real retry's success is not late).
func TestLateResultCandidates_RelaunchInBetweenIsNotACandidate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(1500, 0)
	t2 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(ImplementationFailedHeader, t0),
		cmt(ImplementationStartedHeader, t1),
		cmt(ReadyForReviewHeader, t2),
	}
	assert.Empty(t, lateResultCandidates(comments))
}

// The collapsed done-stage board column folds pr-review, deploy and deploy-fix
// together, but a pr-review failure and an unrelated later deploy success are
// different runs. Pairing them would inflate the count with pairs that never
// happened (SC-3853, review round 1/2's cross-substage requirement).
func TestLateResultCandidates_CrossSubstageIsNotACandidate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(PRReviewFailedHeader, t0),
		cmt(DeployedHeader, t1),
	}
	assert.Empty(t, lateResultCandidates(comments))
}

// Rule 2: the confirm-shipped repair pass's own [human:deployed] post looks
// exactly like a late deploy result — deploy-failed followed by deployed with
// no relaunch in between — but it is the repair pass discovering an
// already-merged PR, not a run completing. Its sentinel excludes it.
func TestLateResultCandidates_ShippedOutOfBandRepairIsNotACandidate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(DeployFailedHeader, t0),
		cmt(ShippedOutOfBandDeployedBody("https://example/pr/1"), t1),
	}
	assert.Empty(t, lateResultCandidates(comments))
}

// A genuine deploy agent finishing after being marked failed must still
// count — the sentinel excludes only the repair pass's own post, never a real
// deploy result (SC-3853, pinning both directions of rule 2).
func TestLateResultCandidates_GenuinelyLateDeployStillCounts(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(DeployFailedHeader, t0),
		cmt(DeployedBody("https://example/pr/1"), t1),
	}
	got := lateResultCandidates(comments)
	require.Len(t, got, 1)
	assert.Equal(t, "deploy", got[0].Pair)
}

// A success with no preceding failure at all is not a late result — it is the
// ordinary happy path.
func TestLateResultCandidates_NoFailureIsNotACandidate(t *testing.T) {
	comments := []tracker.Comment{cmt(PlanReadyHeader, time.Unix(1000, 0))}
	assert.Empty(t, lateResultCandidates(comments))
}

// Once a [human:late-result-reconciled] marker records an occurrence, the same
// occurrence must not be flagged again on a later scan.
func TestLateResultAlreadyReconciled_SkipsWhatWasAlreadyRecorded(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	cand := LateResultCandidate{Pair: "review", Stage: BoardVerification, FailedAt: t0, SuccessAt: t1}
	comments := []tracker.Comment{
		cmt(ReviewFailedHeader, t0),
		cmt(ReviewCompleteHeader, t1),
		cmt(LateResultReconciledBody("review", BoardVerification, t0, t1), t1.Add(time.Minute)),
	}
	assert.True(t, lateResultAlreadyReconciled(comments, cand))
}

func TestLateResultAlreadyReconciled_FalseWhenNoneRecorded(t *testing.T) {
	cand := LateResultCandidate{Pair: "review", Stage: BoardVerification, FailedAt: time.Unix(1000, 0), SuccessAt: time.Unix(2000, 0)}
	assert.False(t, lateResultAlreadyReconciled(nil, cand))
}

// fakeLateResultCommenter records posted bodies per key.
type fakeLateResultCommenter struct {
	posted map[string][]string
}

func (f *fakeLateResultCommenter) AddComment(ctx context.Context, key, body string) (*tracker.Comment, error) {
	if f.posted == nil {
		f.posted = map[string][]string{}
	}
	f.posted[key] = append(f.posted[key], body)
	return &tracker.Comment{Body: body, Created: time.Now()}, nil
}

func (f *fakeLateResultCommenter) ListComments(ctx context.Context, key string) ([]tracker.Comment, error) {
	return nil, nil
}

// RunLateResultReconcile posts a record for a genuine late result and never
// reposts it on a subsequent scan (idempotency — the pass runs on a timer).
func TestReconcileLateResultsOnce_PostsOnceThenDedupes(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(ReviewFailedHeader, t0),
		cmt(ReviewCompleteHeader, t1),
	}
	commenter := &fakeLateResultCommenter{}
	list := func(ctx context.Context) ([]ReconcileCard, error) {
		return []ReconcileCard{{Key: "SC-1", Comments: comments}}, nil
	}
	commenterFor := func() (tracker.Commenter, error) { return commenter, nil }

	reconcileLateResultsOnce(context.Background(), list, commenterFor, zerolog.Nop())
	require.Len(t, commenter.posted["SC-1"], 1)

	// Second pass: the posted marker now sits on the thread, so the next scan
	// must not repost.
	comments = append(comments, cmt(commenter.posted["SC-1"][0], t1.Add(time.Minute)))
	list = func(ctx context.Context) ([]ReconcileCard, error) {
		return []ReconcileCard{{Key: "SC-1", Comments: comments}}, nil
	}
	reconcileLateResultsOnce(context.Background(), list, commenterFor, zerolog.Nop())
	assert.Len(t, commenter.posted["SC-1"], 1, "an already-reconciled occurrence must not be reposted")
}

func TestRunLateResultReconcile_NilDepsNoOp(t *testing.T) {
	assert.NotPanics(t, func() {
		RunLateResultReconcile(context.Background(), nil, nil, zerolog.Nop())
	})
}
