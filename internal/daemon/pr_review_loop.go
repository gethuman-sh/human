package daemon

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/tracker"
)

// The pre-merge PR review→fix loop. Once a deploy opens the PR, the daemon runs
// the human-pr-reviewer and human-pr-fixer agents in alternation against it: the
// reviewer records its findings and a verdict, the fixer addresses them and
// commits on the LOCAL branch, and the reviewer re-reads that local commit —
// until the review is clean, a human decision is needed, or the round budget is
// spent. The fixer does not push (board containers hold no push credentials); the
// daemon ships the branch only at merge, so the reviewer reading the local ref
// rather than the stale pushed head is what lets the loop converge at all
// (SC-1760). Human review happens out of band and never gates this loop.
//
// This file is the pure decider: given the step that just finished and its
// recorded outcome, it names the next action. Reading the state and executing
// the action live in the deploy path (Phase 3); keeping the decision pure lets
// every transition — including the budget boundary and the defensive
// escalations — be unit-tested without a daemon.

// DefaultPRReviewRounds is the OUTER bound of the review→fix loop: a safety
// net, not the policy. The policy is repetition — the loop escalates when the
// reviewer's blocking finding is the one the fixer was already sent, because a
// finding that survives a fix round is the reviewer and fixer disagreeing, the
// ping-pong SC-1760 exists to break. A round that finds a different problem is
// progress: on the first four tickets shipped through the machine review every
// round found a new, real problem, and a count of three turned five of those
// rounds into a person's turn (SC-5174). Eight bounds the cost when findings
// keep changing; it should be rare for a loop to reach it without repeating.
const DefaultPRReviewRounds = 8

// MaxSoleDirectionPursuits bounds a DIFFERENT loop that happens to key off the
// same review-round count: a fix-stage escalation naming exactly one
// direction is not a fork (SC-3630) and the daemon pursues it in place — a
// FULL implementation-stage rebuild per pursuit — rather than asking. That
// budget used to reuse DefaultPRReviewRounds, so raising the review loop's
// outer cap for SC-5174 would have silently taken sole-direction pursuit from
// three rebuilds to eight along with it, though its own reasoning (repetition,
// not count, bounds cost) has nothing to say about a loop that is not the
// review→fix loop at all. Named and held at the old value on purpose.
const MaxSoleDirectionPursuits = 3

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
func NextPRLoopAction(stage PRLoopStage, outcome string, round, budget int, repeated bool) PRLoopAction {
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
			if repeated || round >= budget {
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
	var latest tracker.Comment
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
		if !found || commentNewer(c, latest) {
			latest, stage, found = c, s, true
		}
	}
	return stage
}

// LatestMarkerTime returns the Created time of the newest comment whose body
// starts with header, and whether one was found at all. It is the identity
// anchor the cmd-layer state reads settle against (SC-2378): the daemon posts
// a round's started-marker BEFORE launching the agent, and the agent writes
// its report only at the very end of the round, so any state-store write for
// this round is necessarily timestamped at or after the marker — a record
// older than the marker can only be a previous round's leftover.
// commentNewer breaks same-second ties deterministically, the same rule every
// other "latest marker" scan in this package already relies on.
func LatestMarkerTime(comments []tracker.Comment, header string) (time.Time, bool) {
	var latest tracker.Comment
	found := false
	for _, c := range comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), header) {
			continue
		}
		if !found || commentNewer(c, latest) {
			latest, found = c, true
		}
	}
	if !found {
		return time.Time{}, false
	}
	return latest.Created, true
}

// findingFingerprintEmDash is the separator the reviewer prompt
// (human-pr-reviewer-agent.md) mandates between a blocking finding's stable
// anchor+slug and its free-form explanation: `BLOCKING <file>:<line> —
// <slug> — <explanation>`. Kept as a named constant because it is a contract
// between this file and that prompt, not an arbitrary formatting choice.
const findingFingerprintEmDash = "—"

// FindingFingerprint reduces a reviewer's findings text to the identity of its
// first blocking finding, so the SAME identity survives a fixer rephrasing
// its explanation around a surviving problem, or reordering findings.
//
// The reviewer's prompt requires a blocking finding to lead with
// `BLOCKING <file>:<line> — <slug> — <explanation>`, and to keep `<file>` and
// `<slug>` byte-identical across rounds while the same underlying problem
// persists — the prompt explicitly frees `<explanation>` to vary ("still not
// fixed", a shifted line, more detail). The line number is part of that free
// half in practice: a fix round that edits the file and fails to fix the
// problem is exactly what shifts it, so the line number is dropped from the
// anchor before comparing — only `<file>` survives from that segment. When
// the first line that STARTS WITH "BLOCKING" (matching the prompt's mandated
// lead-in, not merely containing the word — "Non-blocking:" must never be
// mistaken for it) follows the `<file>:<line> — <slug>` shape, the
// fingerprint is `<file> — <slug>`, normalized. Text that does not — an
// older thread, a verdict with "no blocking issues", a reviewer that skipped
// the convention — falls back to the whole line, lower-cased,
// whitespace-collapsed and cut to 160 characters, exactly as before: still an
// identity, just a weaker one, and never a crash.
func FindingFingerprint(findings string) string {
	chosen := ""
	for _, line := range strings.Split(findings, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if chosen == "" {
			chosen = line
		}
		if strings.HasPrefix(strings.ToUpper(line), "BLOCKING") {
			chosen = line
			break
		}
	}
	if chosen == "" {
		return ""
	}
	if parts := strings.SplitN(chosen, findingFingerprintEmDash, 3); len(parts) >= 2 {
		anchor := normalizeFingerprintText(parts[0])
		anchor = strings.TrimSpace(strings.TrimPrefix(anchor, "blocking"))
		anchor = anchorFileOnly(anchor)
		slug := normalizeFingerprintText(parts[1])
		if anchor != "" && slug != "" {
			return cutFingerprintRunes(anchor + " " + findingFingerprintEmDash + " " + slug)
		}
	}
	return cutFingerprintRunes(normalizeFingerprintText(chosen))
}

// anchorFileOnly strips a trailing `:<line>` from a `<file>:<line>` anchor so
// the fingerprint survives the line moving between rounds — see
// FindingFingerprint. Anchors without a colon (already just a file, or some
// other shape) pass through unchanged.
func anchorFileOnly(anchor string) string {
	if i := strings.LastIndex(anchor, ":"); i >= 0 {
		return anchor[:i]
	}
	return anchor
}

// normalizeFingerprintText lower-cases and whitespace-collapses a fragment so
// two rounds' incidental formatting differences (extra spaces, case) never
// break an otherwise-identical fingerprint.
func normalizeFingerprintText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// cutFingerprintRunes bounds a fingerprint's length — 160 runes, the limit
// this format has always used — so a pathologically long line cannot grow the
// state store's comparison key without bound.
func cutFingerprintRunes(s string) string {
	if r := []rune(s); len(r) > 160 {
		return string(r[:160])
	}
	return s
}

// lastFixFinding is the fingerprint the newest pr-fix-started marker recorded:
// the finding the fixer was last sent. Empty when the loop has not fixed yet
// or the marker predates the field.
func lastFixFinding(comments []tracker.Comment) string {
	return strings.TrimSpace(latestPrefixedLine(comments, PRFixStartedHeader, "finding:"))
}

// findingRepeated reports that this round's blocking finding is the one the
// fixer was already sent.
func findingRepeated(comments []tracker.Comment, finding string) bool {
	return finding != "" && finding == lastFixFinding(comments)
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

// PRLoopOutcome is what the loop step that just finished left behind: the
// reviewer's verdict or the fixer's exit, plus whether either was RECORDED AT
// ALL. That last distinction is the point of the type. An agent that crashed
// before writing anything and an agent that deliberately reported something the
// daemon cannot classify both escalate, but they are different failures and the
// ticket must be able to say which — the same distinction mayRelaunch already
// draws for ordinary stages (board_retry.go). Carried as one value rather than
// six positional arguments, which is how the recorded/unrecorded pairs stayed
// impossible to tell apart.
//
// Agent and ErrorType identify the exited run so the escalation can carry a real
// diagnosis. Both are empty when the loop is re-driven by the durable reconcile
// pass, where the agent is long gone — the marker then falls back to its generic
// line, which is the honest answer there.
type PRLoopOutcome struct {
	ReviewVerdict  string
	ReviewRecorded bool
	// ReviewHead is the branch-tip SHA the reviewer actually read. Under the
	// local-ref review model the reviewer reviews the fixer's LOCAL commit, so
	// this is the local branch tip, not origin's — the pushed head is stale by
	// design until the daemon ships the branch at merge (SC-1760).
	ReviewHead  string
	FixExit     string
	FixRecorded bool
	// FixHead is the branch-tip SHA the fixer left behind. When it equals the
	// head the preceding review already read, the fixer produced no new commit
	// and a re-review would only reproduce the same findings — the convergence
	// guard escalates instead of looping.
	FixHead    string
	FixOptions []BoardOption
	FixSummary string
	Agent      string
	ErrorType  string
	// ReviewFinding is the fingerprint of the reviewer's blocking finding
	// (FindingFingerprint over the report's findings), the identity the
	// repetition bound compares across rounds.
	ReviewFinding string
	// FindingRepeated reports that ReviewFinding is the finding the fixer was
	// sent last round — recorded on the pr-fix-started marker — so the round
	// changed nothing the reviewer could see. Set by the executor from the
	// thread; the pure decider only reads it.
	FindingRepeated bool
	// ReviewStale/FixStale report that the corresponding record above was NOT
	// confirmed to be this round's own write — the cmd-layer reader raced ahead
	// of the reviewer/fixer's final write and, after its bounded settle backoff,
	// still could not tell whether it was reading this step's outcome or the
	// previous round's leftover. A stale record must never be acted on: the loop
	// escalates instead of trusting a verdict/exit it cannot confirm is current
	// (SC-2378).
	ReviewStale bool
	FixStale    bool
}

// headStalled reports the convergence-guard condition: the fixer finished but the
// branch tip it left is the very SHA the preceding review already read, so the
// fixer added no commit. Re-reviewing an unchanged head is the exact non-
// converging loop SC-1760 exists to break — it must escalate loudly, not spin.
// A missing FixHead is not a stall: it means the fixer did not record a head, and
// the loop's other rules (unrecorded/needs-input) decide that case.
func (o PRLoopOutcome) headStalled() bool {
	return o.FixHead != "" && o.FixHead == o.ReviewHead
}

// stepRecorded reports whether the step that just ran recorded its outcome.
// PRStageNone has no step behind it, so nothing is missing.
func (o PRLoopOutcome) stepRecorded(stage PRLoopStage) bool {
	switch stage {
	case PRStageReview:
		return o.ReviewRecorded
	case PRStageFix:
		return o.FixRecorded
	default:
		return true
	}
}

// stepStale reports whether the just-finished step's own record was NOT
// confirmed to be this round's write — see ReviewStale/FixStale. PRStageNone
// has no step behind it, so nothing can be stale.
func (o PRLoopOutcome) stepStale(stage PRLoopStage) bool {
	switch stage {
	case PRStageReview:
		return o.ReviewStale
	case PRStageFix:
		return o.FixStale
	default:
		return false
	}
}

// EvaluatePRLoop bridges the recorded board state to the decider: it reads which
// loop step last ran (from the markers) and how many review rounds have
// completed, pairs the step with the outcome that step recorded — the reviewer's
// verdict or the fixer's exit, which live in the state store, not the comment
// thread, so the caller supplies them via `outcome` — and returns the next
// action. Keeping the bridge pure lets the marker/state → action mapping be
// tested without a daemon; the caller executes the action (launch an agent,
// mark-ready + merge, or red the card).
//
// On top of the pure transition it enforces two further rules, checked in
// order.
//
// First, staleness (SC-2378): the loop never acts on a step's outcome until
// that step's own record is the one being read. If the just-finished step's
// record could not be confirmed as THIS round's write — the cmd-layer reader
// raced ahead of the reviewer/fixer's final write — it escalates immediately,
// before the outcome is even looked at, rather than risk treating a
// superseded verdict/exit as current.
//
// Second, the convergence guard: a fix that finished `done` but left the
// branch tip on the SAME SHA the preceding review read (headStalled) means
// the fixer added no commit. When the preceding review had already APPROVED,
// that is not a failure to converge — there was nothing left to fix — so it
// merges (SC-2307/AD3); any other preceding verdict is a genuine
// non-convergence and still escalates rather than re-reviewing forever
// (SC-1760).
func EvaluatePRLoop(comments []tracker.Comment, outcome PRLoopOutcome) PRLoopAction {
	stage := latestPRLoopStage(comments)
	if outcome.stepStale(stage) {
		return PRActionEscalate
	}
	var step string
	switch stage {
	case PRStageReview:
		step = outcome.ReviewVerdict
	case PRStageFix:
		step = outcome.FixExit
	}
	action := NextPRLoopAction(stage, step, prReviewRounds(comments), DefaultPRReviewRounds, outcome.FindingRepeated)
	if stage == PRStageFix && action == PRActionReview && outcome.headStalled() {
		if outcome.ReviewVerdict == PRVerdictApproved {
			return PRActionMerge
		}
		return PRActionEscalate
	}
	return action
}
