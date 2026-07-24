package daemon

import "testing"

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
