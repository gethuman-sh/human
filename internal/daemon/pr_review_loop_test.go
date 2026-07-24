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
		{"fix needs-input escalates", PRStageFix, ExitNeedsInput, 1, 3, PRActionEscalate},
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

func TestEvaluatePRLoop(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)

	rev := func(at time.Time) tracker.Comment { return cmt(PRReviewStartedHeader, at) }
	fix := func(at time.Time) tracker.Comment { return cmt(PRFixStartedHeader, at) }

	cases := []struct {
		name     string
		comments []tracker.Comment
		verdict  string
		fixExit  string
		want     PRLoopAction
	}{
		{"no loop markers reviews first", nil, "", "", PRActionReview},
		{"review approved merges", []tracker.Comment{rev(t0)}, PRVerdictApproved, "", PRActionMerge},
		{"review changes below budget fixes", []tracker.Comment{rev(t0)}, PRVerdictChanges, "", PRActionFix},
		{"review changes at budget escalates",
			[]tracker.Comment{rev(t0), rev(t1), rev(t2)}, PRVerdictChanges, "", PRActionEscalate},
		{"fix done re-reviews",
			[]tracker.Comment{rev(t0), fix(t1)}, "", PRFixDone, PRActionReview},
		{"fix needs-input escalates",
			[]tracker.Comment{rev(t0), fix(t1)}, "", ExitNeedsInput, PRActionEscalate},
		// The newest loop marker names the step that just finished, so a fix after
		// a review is evaluated as a fix (its exit), not the stale review verdict.
		{"latest marker decides the step",
			[]tracker.Comment{rev(t1), fix(t2)}, PRVerdictApproved, PRFixDone, PRActionReview},
		// Deploy-stage markers share the done stage but never move the loop.
		{"deploy markers are ignored",
			[]tracker.Comment{cmt(DeployStartedHeader, t0), rev(t1)}, PRVerdictApproved, "", PRActionMerge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluatePRLoop(tc.comments, tc.verdict, tc.fixExit)
			if got != tc.want {
				t.Fatalf("EvaluatePRLoop() = %d, want %d", got, tc.want)
			}
		})
	}
}
