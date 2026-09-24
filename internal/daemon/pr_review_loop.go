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
// problem is exactly what shifts it, so only `<file>` survives from the
// anchor. The identity is `<file> — <slug>`, normalized.
//
// A finding is recognised wherever a reviewer puts it — as a bullet, a
// numbered item, under a heading — so leading list and heading markers are
// stripped before the lead-in test; the lead-in must then be the word
// BLOCKING itself, so "Non-blocking:" is never mistaken for it. Text with no
// such line, or a BLOCKING line without the three-part shape (an older thread,
// "no blocking issues", a reviewer that skipped the convention), yields NO
// identity: "" is never recorded as a finding and never counts as repeated, so
// such a round can end only on the outer round cap. A weaker identity stood in
// here once and made two different findings collide on their shared preamble
// — the premature escalation this bound exists to remove (SC-5174).
func FindingFingerprint(findings string) string {
	parsed := ParseFindings(findings)
	if len(parsed) == 0 {
		return ""
	}
	return parsed[0].Fingerprint()
}

// Finding is one blocking finding as the reviewer's prompt shapes it:
// `BLOCKING <file>:<line> — <slug> — [<class>] <explanation>`. File and Slug
// are normalized the way the fingerprint always was; Class is the bracketed
// category the explanation leads with, lower-cased, or "" when the reviewer
// named none (an older thread, a reviewer that skipped it).
type Finding struct {
	File  string
	Slug  string
	Class string
	Text  string
}

// Fingerprint is the finding's identity across rounds: `<file> — <slug>`.
func (f Finding) Fingerprint() string {
	return cutFingerprintRunes(f.File + " " + findingFingerprintEmDash + " " + f.Slug)
}

// ClassKey is the finding's identity by class: `<file> — <class>`, "" when it
// carries no class. Two findings with different slugs but the same class in
// the same file are the same defect reported under a fresh description, which
// is what made one log-injection defect cost three rounds (SC-5278).
func (f Finding) ClassKey() string {
	if f.Class == "" {
		return ""
	}
	return cutFingerprintRunes(f.File + " " + findingFingerprintEmDash + " " + f.Class)
}

// ParseFindings reads every well-formed blocking finding out of a reviewer's
// findings text, in order. The first is what FindingFingerprint reports; the
// whole list is what the durable findings record keeps. A BLOCKING line
// without the three-part shape ends the scan with what was read so far — as
// FindingFingerprint always treated it: no identity, never repeated.
func ParseFindings(findings string) []Finding {
	var out []Finding
	for _, line := range strings.Split(findings, "\n") {
		line = stripListMarkers(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(strings.TrimRight(fields[0], ":"), "blocking") {
			continue
		}
		f, ok := parseFindingLine(line)
		if !ok {
			return out
		}
		out = append(out, f)
	}
	return out
}

func parseFindingLine(line string) (Finding, bool) {
	parts := strings.SplitN(line, findingFingerprintEmDash, 3)
	if len(parts) < 2 {
		return Finding{}, false
	}
	anchor := normalizeFingerprintText(parts[0])
	anchor = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(anchor, "blocking:"), "blocking"))
	anchor = anchorFileOnly(anchor)
	slug := normalizeFingerprintText(parts[1])
	if anchor == "" || slug == "" {
		return Finding{}, false
	}
	f := Finding{File: anchor, Slug: slug}
	if len(parts) == 3 {
		f.Class, f.Text = splitFindingClass(strings.TrimSpace(parts[2]))
	}
	return f, true
}

// splitFindingClass reads the `[<class>]` the explanation leads with. The
// token is one word of letters and hyphens; anything else is explanation text.
func splitFindingClass(text string) (class, rest string) {
	if !strings.HasPrefix(text, "[") {
		return "", text
	}
	end := strings.Index(text, "]")
	if end < 0 {
		return "", text
	}
	token := strings.ToLower(strings.TrimSpace(text[1:end]))
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", text
	}
	return token, strings.TrimSpace(text[end+1:])
}

// stripListMarkers removes the markdown a reviewer may wrap a finding in — a
// bullet, a numbered item, a heading — so the lead-in test sees the finding
// itself. Repeated so a bullet under a heading on one line still resolves.
func stripListMarkers(line string) string {
	for {
		trimmed := strings.TrimLeft(line, "#")
		if trimmed != line {
			line = strings.TrimSpace(trimmed)
			continue
		}
		if len(line) > 1 && strings.ContainsRune("-*+", rune(line[0])) && line[1] == ' ' {
			line = strings.TrimSpace(line[2:])
			continue
		}
		if i := strings.IndexAny(line, ".)"); i > 0 && i < 4 && strings.Trim(line[:i], "0123456789") == "" && i+1 < len(line) && line[i+1] == ' ' {
			line = strings.TrimSpace(line[i+2:])
			continue
		}
		return line
	}
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

// lastFixClass is the class key the newest pr-fix-started marker recorded:
// the (file, class) of the finding the fixer was last sent. Empty when the
// finding carried no class or the marker predates the field.
func lastFixClass(comments []tracker.Comment) string {
	return strings.TrimSpace(latestPrefixedLine(comments, PRFixStartedHeader, "class:"))
}

// classRepeated reports that this round's blocking finding is of the class,
// in the file, the fixer was already sent — the same defect under a fresh
// slug. A finding without a class never repeats by class, as a finding
// without a fingerprint never repeats by identity.
func classRepeated(comments []tracker.Comment, classKey string) bool {
	return classKey != "" && classKey == lastFixClass(comments)
}

// PRReviewRounds is the number of review rounds the thread has started — the
// round a reviewer's report belongs to, for the durable findings record.
func PRReviewRounds(comments []tracker.Comment) int { return prReviewRounds(comments) }

// PRLoopNumber is the pull request the loop is running on, 0 when the thread
// names none.
func PRLoopNumber(comments []tracker.Comment) int { return prLoopNumber(comments) }

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
	// ReviewClass is the class key of that finding (Finding.ClassKey: the file
	// and the reviewer's category), the second identity the repetition bound
	// compares — "" when the reviewer named no class.
	ReviewClass string
	// FindingRepeated reports that ReviewFinding is the finding the fixer was
	// sent last round — recorded on the pr-fix-started marker — or that
	// ReviewClass is the class the fixer was sent in the same file, so the
	// round changed nothing the reviewer could see. Set by the executor from
	// the thread; the pure decider only reads it.
	FindingRepeated bool
	// ClassRepeated narrows FindingRepeated: the identity differed but the
	// class in the file did not, so the escalation names the class rather than
	// a slug the fixer never saw twice.
	ClassRepeated bool
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
