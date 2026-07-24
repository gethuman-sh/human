package daemon

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/tracker"
)

// The pre-merge PR review→fix loop. Once a deploy opens the PR, the daemon runs
// the human-pr-reviewer and human-pr-fixer agents in alternation against it: the
// reviewer posts its findings as PR comments and records a verdict, the fixer
// addresses them and pushes, and the reviewer runs again — until the review is
// clean, a human decision is needed, or the round budget is spent. Human review
// happens out of band and never gates this loop.
//
// This file is the pure decider: given the step that just finished and its
// recorded outcome, it names the next action. Reading the state and executing
// the action live in the deploy path (Phase 3); keeping the decision pure lets
// every transition — including the budget boundary and the defensive
// escalations — be unit-tested without a daemon.

// DefaultPRReviewRounds bounds the review→fix loop: at most this many review
// rounds before a still-unresolved review escalates to a human. A budget is
// mandatory — without it a reviewer and fixer that disagree would ping-pong
// forever, and each round costs an agent run plus a fresh CI trigger.
const DefaultPRReviewRounds = 3

// PR review/fix outcomes the decider branches on. These mirror the vocabulary
// the human-pr-reviewer and human-pr-fixer prompts record in state, kept here as
// the single Go-side source of truth. The fixer's needs-input reuses the shared
// ExitNeedsInput; only "done" advances, everything else is treated as escalate.
const (
	PRVerdictApproved     = "approved"
	PRVerdictChanges      = "changes-requested"
	PRVerdictUnreviewable = "unreviewable"
	PRFixDone             = "done"
)

// PRLoopStage names the loop step that just completed (PRStageNone when none
// has: the PR is freshly opened and no review has run).
type PRLoopStage int

const (
	PRStageNone PRLoopStage = iota
	PRStageReview
	PRStageFix
)

// PRLoopAction is the next step the deploy path should take.
type PRLoopAction int

const (
	PRActionReview   PRLoopAction = iota // run human-pr-reviewer
	PRActionFix                          // run human-pr-fixer
	PRActionMerge                        // review is clean — proceed to the CI gate + merge
	PRActionEscalate                     // stop and leave the card for a human
)

// NextPRLoopAction is the loop's transition function. `stage` is the step that
// just finished and `outcome` its recorded field — the reviewer's verdict
// (approved | changes-requested | unreviewable) or the fixer's exit
// (done | needs-input). `round` is the number of reviews completed so far and
// `budget` the maximum (DefaultPRReviewRounds when non-positive).
//
// Two safety rules are baked in. An unrecognized outcome escalates rather than
// proceeds: the loop must never merge on a state it cannot read. And a
// changes-requested review at the round budget escalates instead of fixing
// again, so a disagreement the fixer cannot close reaches a human in bounded
// time rather than looping.
func NextPRLoopAction(stage PRLoopStage, outcome string, round, budget int) PRLoopAction {
	if budget <= 0 {
		budget = DefaultPRReviewRounds
	}
	switch stage {
	case PRStageNone:
		return PRActionReview
	case PRStageReview:
		switch outcome {
		case PRVerdictApproved:
			return PRActionMerge
		case PRVerdictChanges:
			if round >= budget {
				return PRActionEscalate
			}
			return PRActionFix
		default: // unreviewable, or an outcome the daemon cannot classify
			return PRActionEscalate
		}
	case PRStageFix:
		if outcome == PRFixDone {
			return PRActionReview
		}
		return PRActionEscalate // needs-input, or unclassifiable
	default:
		return PRActionEscalate
	}
}

// latestPRLoopStage reports which loop step most recently started — and so just
// finished, when its agent's Stop fires the evaluation. It scans the comment
// thread for the newest pr-review-started / pr-fix-started marker; PRStageNone
// means the loop has not run yet (the draft PR is freshly opened). Deploy-stage
// markers that share the done stage are ignored: only the loop's own markers
// move the loop.
func latestPRLoopStage(comments []tracker.Comment) PRLoopStage {
	stage := PRStageNone
	var latest time.Time
	found := false
	for _, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		var s PRLoopStage
		switch {
		case strings.HasPrefix(trimmed, PRReviewStartedHeader):
			s = PRStageReview
		case strings.HasPrefix(trimmed, PRFixStartedHeader):
			s = PRStageFix
		default:
			continue
		}
		if !found || c.Created.After(latest) {
			latest, stage, found = c.Created, s, true
		}
	}
	return stage
}

// prReviewRounds counts completed review rounds — one per pr-review-started
// marker — the value the decider bounds against DefaultPRReviewRounds.
func prReviewRounds(comments []tracker.Comment) int {
	n := 0
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), PRReviewStartedHeader) {
			n++
		}
	}
	return n
}

// EvaluatePRLoop bridges the recorded board state to the decider: it reads which
// loop step last ran (from the markers) and how many review rounds have
// completed, pairs the step with the outcome that step recorded — the reviewer's
// verdict or the fixer's exit, which live in the state store, not the comment
// thread, so the caller supplies them — and returns the next action. Keeping the
// bridge pure lets the marker/state → action mapping be tested without a daemon;
// the caller executes the action (launch an agent, mark-ready + merge, or red
// the card).
func EvaluatePRLoop(comments []tracker.Comment, reviewVerdict, fixExit string) PRLoopAction {
	stage := latestPRLoopStage(comments)
	var outcome string
	switch stage {
	case PRStageReview:
		outcome = reviewVerdict
	case PRStageFix:
		outcome = fixExit
	}
	return NextPRLoopAction(stage, outcome, prReviewRounds(comments), DefaultPRReviewRounds)
}
