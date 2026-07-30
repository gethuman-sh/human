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
		pushed  bool
		round   int
		budget  int
		want    PRLoopAction
	}{
		// The loop opens with a review of the freshly-opened PR.
		{"fresh PR reviews first", PRStageNone, "", false, 0, 3, PRActionReview},

		// A clean review is the only path to merge.
		{"approved proceeds to merge", PRStageReview, PRVerdictApproved, false, 1, 3, PRActionMerge},

		// Changes-requested with budget left runs the fixer.
		{"changes below budget fixes", PRStageReview, PRVerdictChanges, false, 1, 3, PRActionFix},
		{"changes one below budget fixes", PRStageReview, PRVerdictChanges, false, 2, 3, PRActionFix},

		// At (or past) the budget, an unresolved review escalates instead of
		// looping — the disagreement the fixer cannot close reaches a human.
		{"changes at budget escalates", PRStageReview, PRVerdictChanges, false, 3, 3, PRActionEscalate},
		{"changes past budget escalates", PRStageReview, PRVerdictChanges, false, 4, 3, PRActionEscalate},

		// Unreviewable / unknown review outcomes never merge — they escalate.
		{"unreviewable escalates", PRStageReview, PRVerdictUnreviewable, false, 1, 3, PRActionEscalate},
		{"unknown review outcome escalates", PRStageReview, "garbage", false, 1, 3, PRActionEscalate},
		{"empty review outcome escalates", PRStageReview, "", false, 1, 3, PRActionEscalate},

		// A completed fix that pushed re-reviews the pushed changes.
		{"fix done and pushed re-reviews", PRStageFix, PRFixDone, true, 1, 3, PRActionReview},
		// Convergence guard: a fix that completed but did not push left the
		// reviewed head unchanged, so re-reviewing it would spin forever —
		// escalate instead (SC-1760).
		{"fix done but not pushed escalates", PRStageFix, PRFixDone, false, 1, 3, PRActionEscalate},
		// A non-done fix escalates regardless of the pushed flag.
		{"fix needs-input escalates", PRStageFix, ExitNeedsInput, false, 1, 3, PRActionEscalate},
		{"fix needs-input escalates even if pushed", PRStageFix, ExitNeedsInput, true, 1, 3, PRActionEscalate},
		{"unknown fix outcome escalates", PRStageFix, "garbage", true, 1, 3, PRActionEscalate},
		{"empty fix outcome escalates", PRStageFix, "", true, 1, 3, PRActionEscalate},

		// A non-positive budget falls back to DefaultPRReviewRounds.
		{"default budget still fixes below cap", PRStageReview, PRVerdictChanges, false, DefaultPRReviewRounds - 1, 0, PRActionFix},
		{"default budget escalates at cap", PRStageReview, PRVerdictChanges, false, DefaultPRReviewRounds, 0, PRActionEscalate},

		// An unrecognized stage escalates rather than guessing.
		{"unknown stage escalates", PRLoopStage(99), PRVerdictApproved, false, 1, 3, PRActionEscalate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextPRLoopAction(tc.stage, tc.outcome, tc.pushed, tc.round, tc.budget)
			if got != tc.want {
				t.Fatalf("NextPRLoopAction(%v, %q, pushed=%v, round=%d, budget=%d) = %d, want %d",
					tc.stage, tc.outcome, tc.pushed, tc.round, tc.budget, got, tc.want)
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
		pushed   bool
		want     PRLoopAction
	}{
		{"no loop markers reviews first", nil, "", "", false, PRActionReview},
		{"review approved merges", []tracker.Comment{rev(t0)}, PRVerdictApproved, "", false, PRActionMerge},
		{"review changes below budget fixes", []tracker.Comment{rev(t0)}, PRVerdictChanges, "", false, PRActionFix},
		{"review changes at budget escalates",
			[]tracker.Comment{rev(t0), rev(t1), rev(t2)}, PRVerdictChanges, "", false, PRActionEscalate},
		{"fix done and pushed re-reviews",
			[]tracker.Comment{rev(t0), fix(t1)}, "", PRFixDone, true, PRActionReview},
		// The convergence guard reaches through the bridge: a fix that completed
		// without pushing escalates rather than re-reviewing the unchanged head.
		{"fix done but not pushed escalates",
			[]tracker.Comment{rev(t0), fix(t1)}, "", PRFixDone, false, PRActionEscalate},
		{"fix needs-input escalates",
			[]tracker.Comment{rev(t0), fix(t1)}, "", ExitNeedsInput, false, PRActionEscalate},
		// The newest loop marker names the step that just finished, so a fix after
		// a review is evaluated as a fix (its exit), not the stale review verdict.
		{"latest marker decides the step",
			[]tracker.Comment{rev(t1), fix(t2)}, PRVerdictApproved, PRFixDone, true, PRActionReview},
		// Deploy-stage markers share the done stage but never move the loop.
		{"deploy markers are ignored",
			[]tracker.Comment{cmt(DeployStartedHeader, t0), rev(t1)}, PRVerdictApproved, "", false, PRActionMerge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluatePRLoop(tc.comments, tc.verdict, tc.fixExit, tc.pushed)
			if got != tc.want {
				t.Fatalf("EvaluatePRLoop() = %d, want %d", got, tc.want)
			}
		})
	}
}
