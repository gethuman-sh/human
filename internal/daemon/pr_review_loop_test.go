package daemon

import (
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestNextPRLoopAction(t *testing.T) {
	cases := []struct {
		name    string
		stage   PRLoopStage
		outcome string
		round   int
		budget  int
		want    PRLoopAction
	}{
		// The loop opens with a review of the freshly-opened PR.
		{"fresh PR reviews first", PRStageNone, "", 0, 3, PRActionReview},

		// A clean review is the only path to merge.
		{"approved proceeds to merge", PRStageReview, PRVerdictApproved, 1, 3, PRActionMerge},

		// Changes-requested with budget left runs the fixer.
		{"changes below budget fixes", PRStageReview, PRVerdictChanges, 1, 3, PRActionFix},
		{"changes one below budget fixes", PRStageReview, PRVerdictChanges, 2, 3, PRActionFix},

		// At (or past) the budget, an unresolved review escalates instead of
		// looping — the disagreement the fixer cannot close reaches a human.
		{"changes at budget escalates", PRStageReview, PRVerdictChanges, 3, 3, PRActionEscalate},
		{"changes past budget escalates", PRStageReview, PRVerdictChanges, 4, 3, PRActionEscalate},

		// Unreviewable / unknown review outcomes never merge — they escalate.
		{"unreviewable escalates", PRStageReview, PRVerdictUnreviewable, 1, 3, PRActionEscalate},
		{"unknown review outcome escalates", PRStageReview, "garbage", 1, 3, PRActionEscalate},
		{"empty review outcome escalates", PRStageReview, "", 1, 3, PRActionEscalate},

		// A completed fix re-reviews the pushed changes; anything else escalates.
		{"fix done re-reviews", PRStageFix, PRFixDone, 1, 3, PRActionReview},
		{"fix needs-input escalates", PRStageFix, string(ExitNeedsInput), 1, 3, PRActionEscalate},
		{"unknown fix outcome escalates", PRStageFix, "garbage", 1, 3, PRActionEscalate},
		{"empty fix outcome escalates", PRStageFix, "", 1, 3, PRActionEscalate},

		// A non-positive budget falls back to DefaultPRReviewRounds.
		{"default budget still fixes below cap", PRStageReview, PRVerdictChanges, DefaultPRReviewRounds - 1, 0, PRActionFix},
		{"default budget escalates at cap", PRStageReview, PRVerdictChanges, DefaultPRReviewRounds, 0, PRActionEscalate},

		// An unrecognized stage escalates rather than guessing.
		{"unknown stage escalates", PRLoopStage(99), PRVerdictApproved, 1, 3, PRActionEscalate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextPRLoopAction(tc.stage, tc.outcome, tc.round, tc.budget)
			if got != tc.want {
				t.Fatalf("NextPRLoopAction(%v, %q, round=%d, budget=%d) = %d, want %d",
					tc.stage, tc.outcome, tc.round, tc.budget, got, tc.want)
			}
		})
	}
}

// TestLatestMarkerTime proves the identity anchor (SC-2378/AD2): it picks the
// NEWEST comment matching the given header (deterministic same-second
// ordering via commentNewer, mirroring every other "latest marker" scan in
// this package) and reports absence honestly when no comment matches.
func TestLatestMarkerTime(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, t0),
		cmt(PRFixStartedHeader, t1),
		cmt(PRReviewStartedHeader, t1.Add(time.Hour)),
	}

	got, ok := LatestMarkerTime(comments, PRReviewStartedHeader)
	if !ok || !got.Equal(t1.Add(time.Hour)) {
		t.Fatalf("LatestMarkerTime() = %v, %v; want %v, true", got, ok, t1.Add(time.Hour))
	}

	if _, ok := LatestMarkerTime(comments, PRReviewFailedHeader); ok {
		t.Fatalf("LatestMarkerTime() found a match for a header with none present")
	}
}

func TestEvaluatePRLoop(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)

	rev := func(at time.Time) tracker.Comment { return cmt(PRReviewStartedHeader, at) }
	fix := func(at time.Time) tracker.Comment { return cmt(PRFixStartedHeader, at) }

	cases := []struct {
		name     string
		comments []tracker.Comment
		outcome  PRLoopOutcome
		want     PRLoopAction
	}{
		{"no loop markers reviews first", nil, PRLoopOutcome{}, PRActionReview},
		{"review approved merges", []tracker.Comment{rev(t0)}, PRLoopOutcome{ReviewVerdict: PRVerdictApproved}, PRActionMerge},
		{"review changes below budget fixes", []tracker.Comment{rev(t0)}, PRLoopOutcome{ReviewVerdict: PRVerdictChanges}, PRActionFix},
		{"review changes at budget escalates",
			[]tracker.Comment{rev(t0), rev(t1), rev(t2)}, PRLoopOutcome{ReviewVerdict: PRVerdictChanges}, PRActionEscalate},
		{"fix done re-reviews",
			[]tracker.Comment{rev(t0), fix(t1)}, PRLoopOutcome{FixExit: PRFixDone}, PRActionReview},
		{"fix needs-input escalates",
			[]tracker.Comment{rev(t0), fix(t1)}, PRLoopOutcome{FixExit: string(ExitNeedsInput)}, PRActionEscalate},
		// The newest loop marker names the step that just finished, so a fix after
		// a review is evaluated as a fix (its exit), not the stale review verdict.
		{"latest marker decides the step",
			[]tracker.Comment{rev(t1), fix(t2)}, PRLoopOutcome{ReviewVerdict: PRVerdictApproved, FixExit: PRFixDone}, PRActionReview},
		// Deploy-stage markers share the done stage but never move the loop.
		{"deploy markers are ignored",
			[]tracker.Comment{cmt(DeployStartedHeader, t0), rev(t1)}, PRLoopOutcome{ReviewVerdict: PRVerdictApproved}, PRActionMerge},

		// Convergence guard (SC-1760): a done fix that left the branch tip on the
		// SHA the preceding review already read added no commit — re-reviewing it
		// would loop, so it escalates.
		{"fix done with unchanged head escalates",
			[]tracker.Comment{rev(t0), fix(t1)},
			PRLoopOutcome{FixExit: PRFixDone, ReviewHead: "abc123", FixHead: "abc123"}, PRActionEscalate},
		// A done fix that advanced the head re-reviews the new commit as normal.
		{"fix done with a new head re-reviews",
			[]tracker.Comment{rev(t0), fix(t1)},
			PRLoopOutcome{FixExit: PRFixDone, ReviewHead: "abc123", FixHead: "def456"}, PRActionReview},
		// An empty FixHead is not a stall — the fixer recorded no head, so the
		// ordinary done→review rule stands rather than a false convergence trip.
		{"fix done without a recorded head re-reviews",
			[]tracker.Comment{rev(t0), fix(t1)},
			PRLoopOutcome{FixExit: PRFixDone, ReviewHead: "abc123", FixHead: ""}, PRActionReview},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluatePRLoop(tc.comments, tc.outcome)
			if got != tc.want {
				t.Fatalf("EvaluatePRLoop() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEvaluatePRLoop_headStalledAfterApproval_merges is the SC-2307 recovery
// case (AD3): a fixer that finished done but left the branch tip unchanged
// after a review that had already APPROVED added no commit because there was
// nothing to fix — the branch was already good. Escalating that (the old
// behaviour) reds a card whose PR is approved, green and ready; it must merge
// instead.
func TestEvaluatePRLoop_headStalledAfterApproval_merges(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{cmt(PRReviewStartedHeader, t0), cmt(PRFixStartedHeader, t1)}
	outcome := PRLoopOutcome{
		ReviewVerdict: PRVerdictApproved,
		FixExit:       PRFixDone,
		ReviewHead:    "abc123",
		FixHead:       "abc123",
	}

	got := EvaluatePRLoop(comments, outcome)

	if got != PRActionMerge {
		t.Fatalf("EvaluatePRLoop() = %d, want PRActionMerge (%d)", got, PRActionMerge)
	}
}

// TestEvaluatePRLoop_staleReviewRecord_escalates proves the loop never acts on
// a step's outcome until that step's own record is the one it read: a review
// record flagged stale (superseded by a write the reader raced ahead of) must
// escalate — naming what could not be read fresh — rather than being treated
// as this round's approval and driving a merge on a verdict that was never
// confirmed current (SC-2378).
func TestEvaluatePRLoop_staleReviewRecord_escalates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	comments := []tracker.Comment{cmt(PRReviewStartedHeader, t0)}
	outcome := PRLoopOutcome{
		ReviewVerdict: PRVerdictApproved,
		ReviewStale:   true,
	}

	got := EvaluatePRLoop(comments, outcome)

	if got != PRActionEscalate {
		t.Fatalf("EvaluatePRLoop() = %d, want PRActionEscalate (%d)", got, PRActionEscalate)
	}
}
