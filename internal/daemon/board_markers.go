package daemon

import (
	"strings"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BoardStage is one of the five pipeline columns (plus a synthetic "hidden"
// stage for closed PM tickets that never entered the pipeline). The drag-board
// GUI renders cards into these columns; the daemon derives the stage from the
// [human:…] comment markers a PM ticket carries.
type BoardStage string

const (
	// BoardIdeas holds idea-labeled tickets (see tracker.Issue.IsIdea) —
	// membership comes from the label, never from markers, and the only way
	// out is promotion via ideation (label swap), not a board transition.
	BoardIdeas          BoardStage = "ideas"
	BoardBacklog        BoardStage = "backlog"
	BoardPlanning       BoardStage = "planning"
	BoardImplementation BoardStage = "implementation"
	BoardVerification   BoardStage = "verification"
	BoardDoneStage      BoardStage = "done"
	BoardHidden         BoardStage = "hidden"

	// BoardTicketReview is a PSEUDO-stage: the ticket-review gate that runs as the
	// first phase of the planning dispatch (planPrompt), before any code is
	// planned. It is never a board column and never a ClassifyMarker output — its
	// markers ([human:ticket-review]/[human:ticket-review-started]) map to
	// BoardBacklog — so it deliberately carries no stageRank. It exists only as the
	// stage a [human:options] decision the gate raises names ("stage: ticket-review"),
	// which optionStageAliases resolves back to BoardPlanning so the decision reaches
	// the board and resuming it re-runs the gate (SC-2137).
	BoardTicketReview BoardStage = "ticket-review"
)

// BoardState is the within-stage status of a card: empty for an idle card
// sitting at the head of a stage, running while an agent works the stage,
// done once the stage's success marker lands, failed on an error marker.
type BoardState string

const (
	BoardIdle    BoardState = ""
	BoardRunning BoardState = "running"
	BoardDone    BoardState = "done"
	BoardFailed  BoardState = "failed"
	// BoardResolved is a non-failing, non-done terminal: an autofix run whose
	// triage concluded no fix is warranted (not-a-bug or undetermined). It
	// neither reds the card, chains a review, nor offers a deploy (ticket 405).
	BoardResolved BoardState = "resolved"
	// BoardQueued marks a stage that a recorded decision ([human:option-chosen])
	// has (re)queued but whose relaunched agent has not yet posted its started
	// marker — either the fresh agent is spinning up, or the launch was deferred
	// to a healthy daemon (SC-1320). A non-failing in-progress state: it never
	// reds the card and is superseded the moment the real started marker lands
	// (latest-wins). [human:option-chosen] is not a classified marker, so this
	// state is synthesized in DeriveBoardCard, not mapped from a marker.
	BoardQueued BoardState = "queued"
	// BoardOutage is a NON-failing transient: a stage exited reporting the
	// substrate it needs was unreachable (a credential store timeout, a tracker
	// it could not reach — ExitOutage). Nothing about the work is wrong. It never
	// reds the card and is NOT charged against DefaultStageRetries; the durable
	// reconcile pass relaunches it each interval (the backoff) until the
	// substrate returns and a *-started marker supersedes it (SC-2307).
	BoardOutage BoardState = "outage"
)

// Board marker headers. These mirror the existing review-handoff headers in
// review_handoff.go and follow the same `strings.HasPrefix(trimmed, header)`
// contract: a comment that merely quotes a header mid-body is NOT a marker.
//
// ReadyForReviewHeader is reused as the implementation done-marker and
// ReviewCompleteHeader as the verification done-marker; both are declared in
// review_handoff.go and intentionally NOT redeclared here.
const (
	// Ticket-review markers bracket the gate that runs BEFORE planning, judging
	// whether solving the ticket solves the problem. Both map to the backlog
	// stage: the card has not entered the pipeline yet, and without them it would
	// sit in Backlog looking idle for the gate's whole run — the "nothing is
	// happening" reading the board must never give.
	TicketReviewStartedHeader = "[human:ticket-review-started]"
	TicketReviewedHeader      = "[human:ticket-review]"
	// TicketReviewMarkerType is the value marker.ParseBody puts in .Type for a
	// [human:ticket-review] verdict (the header name without the human: prefix).
	// It is also the key terminalStopVerdicts registers the stop heads under, so
	// downstream readers key off this const rather than a bare literal.
	TicketReviewMarkerType = "ticket-review"

	// The marker TYPE names the daemon posts, and the headers derived from them.
	//
	// Derived rather than written twice: a header and its type name are the same
	// fact spelled two ways, and the protocol's writer takes the type while the
	// board's readers match the header. Two literals would be one edit away from
	// disagreeing.
	MarkerDeployed               = "deployed"
	MarkerDeployFailed           = "deploy-failed"
	MarkerNeedsPlanning          = "needs-planning"
	MarkerPRReviewFailed         = "pr-review-failed"
	MarkerReviewFailed           = "review-failed"
	MarkerPipeline               = "pipeline"
	MarkerOptions                = "options"
	MarkerOptionChosen           = "option-chosen"
	MarkerStageWait              = "stage-wait"
	MarkerClaim                  = "claim"
	MarkerCloseFailed            = "close-failed"
	MarkerRunCancelled           = "run-cancelled"
	MarkerPlanningFailed         = "planning-failed"
	MarkerImplementationFailed   = "implementation-failed"
	MarkerPlanningOutage         = "planning-outage"
	MarkerImplementationOutage   = "implementation-outage"
	MarkerReviewOutage           = "review-outage"
	MarkerDeployOutage           = "deploy-outage"
	MarkerPRReviewStarted        = "pr-review-started"
	MarkerDeployFixStarted       = "deploy-fix-started"
	MarkerHandoffCheckUnreadable = "handoff-check-unreadable"
	// MarkerLateResultReconciled records that a stage's result arrived after the
	// stage had already been marked failed, with no relaunch marker between the
	// two — the reaper (or an equivalent silence classifier) declared a still-
	// working agent dead (SC-3853). Informational only: it decorates the
	// ticket's history and never moves the card (see LateResultReconciledBody).
	MarkerLateResultReconciled = "late-result-reconciled"

	PlanningStartedHeader       = "[human:planning-started]"
	PlanReadyHeader             = "[human:plan-ready]"
	PlanningFailedHeader        = "[human:" + MarkerPlanningFailed + "]"
	ImplementationStartedHeader = "[human:implementation-started]"
	ImplementationFailedHeader  = "[human:" + MarkerImplementationFailed + "]"
	// NeedsPlanningHeader is the implementation launch's refuse-and-surface
	// marker: the stage that exists only to carry out a plan was launched on a
	// ticket that has none, so the launch is refused before any agent claims the
	// ticket (SC-2596). It maps to the PLANNING stage (BoardFailed) so the card
	// surfaces back where a human can trigger planning, rather than sitting on a
	// phantom implementation run that the stuck-running reconcile would later red
	// as a crash. DeriveBoardCard promotes it over the phantom implementation
	// markers it refused (newestTerminalDetermination — it is one of the
	// registered terminalResolutions).
	NeedsPlanningHeader = "[human:" + MarkerNeedsPlanning + "]"
	// NoFixNeededHeader is the autofix pipeline's second clean terminal marker:
	// triage concluded the reported bug warrants no code change (not-a-bug or
	// undetermined). It carries no [human:ready-for-review] handoff, so the
	// failure watcher would otherwise mistake the missing handoff for a crash
	// and loop forever re-triaging (ticket 405).
	NoFixNeededHeader = "[human:no-fix-needed]"
	// NothingToDoHeader is the planning stage's terminal "nothing to plan"
	// marker: the planner verified the ticket's work is already merged, so
	// attaching a [human:plan-ready] plan would advance the card and re-implement
	// shipped code. It carries no plan, so the failure watcher would otherwise
	// mistake the missing [human:plan-ready] for a crash and loop forever
	// re-planning shipped work (ticket 454 — the planning twin of ticket 405).
	// The name is stage-agnostic on purpose: it shares the BoardResolved clean
	// terminal with the implementation stage's [human:no-fix-needed].
	NothingToDoHeader   = "[human:nothing-to-do]"
	ReviewStartedHeader = "[human:review-started]"
	ReviewFailedHeader  = "[human:" + MarkerReviewFailed + "]"
	PRStartedHeader     = "[human:pr-started]"
	PRPushedHeader      = "[human:pr-pushed]"
	PRFailedHeader      = "[human:pr-failed]"

	// Deploy markers supersede the PR markers for the done stage: dropping a
	// card on Deploy runs push → PR → CI gate → merge → close, so the stage's
	// lifecycle is "deploying", not "opening a PR". The PR markers stay
	// recognized so threads written before the deploy pipeline still derive.
	DeployStartedHeader = "[human:deploy-started]"
	DeployedHeader      = "[human:" + MarkerDeployed + "]"
	DeployFailedHeader  = "[human:" + MarkerDeployFailed + "]"

	// PR review→fix loop markers (SC-1387): the pre-merge sub-phase of the
	// deploy (done) stage where the machine reviewer and fixer alternate on the
	// open draft PR before the merge. Both launches read as the done stage
	// running; an escalation (budget spent, unreviewable, or an unresolvable
	// review) reds it like any other deploy-phase failure. They deliberately
	// live in the done stage rather than a new pipeline stage, so the
	// verification→done transition adjacency (board_transition.go) is unchanged.
	PRReviewStartedHeader = "[human:" + MarkerPRReviewStarted + "]"
	PRFixStartedHeader    = "[human:pr-fix-started]"
	PRReviewFailedHeader  = "[human:" + MarkerPRReviewFailed + "]"
	// PRReviewPassedHeader records the loop CONVERGING: the reviewer approved and
	// the card proceeds to the CI gate and merge. Every other outcome of the loop
	// already left a marker — both launches and the escalation — so success was
	// the one transition the thread did not record, and a reader could not tell
	// "the review passed, the merge is running" from "the review is still going".
	// It is also what retires the loop sub-phase: while a *-started marker is the
	// newest done-stage marker the badge reads "PR review…", so without this the
	// card kept claiming a review was in flight for the whole of the CI gate,
	// rebase and merge.
	PRReviewPassedHeader = "[human:pr-review-passed]" // #nosec G101 -- a marker header; "Passed" trips the credential-name heuristic

	// DeployFixStartedHeader marks the deploy stage's automated fixer sub-phase
	// (SC-1557): a CI failure or rebase conflict at the deploy gate dispatches the
	// human-deploy-fixer instead of redding, so this reads as the done stage running.
	// Each occurrence is one deploy-fix round — the budget counts them (deployFixRounds).
	DeployFixStartedHeader = "[human:" + MarkerDeployFixStarted + "]"

	// Outage markers are the NON-failing transient twin of the *-failed headers,
	// one per relaunchable stage. A stage that reported the substrate it needs was
	// unreachable (ExitOutage) posts its stage's *-outage marker instead of a
	// *-failed one, so the card reads "machine down" rather than red and the
	// durable reconcile pass relaunches it each interval without charging the
	// retry budget (SC-2307). `-outage` never prefixes another header, so
	// ClassifyMarker's prefix match stays unambiguous.
	PlanningOutageHeader       = "[human:" + MarkerPlanningOutage + "]"
	ImplementationOutageHeader = "[human:" + MarkerImplementationOutage + "]"
	ReviewOutageHeader         = "[human:" + MarkerReviewOutage + "]"
	DeployOutageHeader         = "[human:" + MarkerDeployOutage + "]"
)

// PlanCommentHeader marks a comment whose body IS the engineering plan for
// this ticket — the single-tracker alternative to a separate engineering
// ticket. It is content, not a stage signal, so it must never join
// orderedMarkerSpecs: the [human:plan-ready] marker still carries the stage
// transition. (ClassifyMarker's prefix matching stays safe because the
// closing bracket keeps "[human:plan]" from matching "[human:plan-ready]".)
const PlanCommentHeader = "[human:plan]"

// CloseFailedHeader flags a ticket whose work shipped but whose automated
// post-merge close failed. It is a surfaced operator signal, NOT a stage
// transition: like PlanCommentHeader it is deliberately kept OUT of
// orderedMarkerSpecs, so ClassifyMarker never classifies it and the card
// stays green (deployed) while still carrying a visible "close this manually"
// flag. Best-effort close means recorded-and-surfaced, never red, never silent.
const CloseFailedHeader = "[human:" + MarkerCloseFailed + "]"

// RunCancelledHeader records that closing a ticket stopped work that was still
// running on it. Closing is cancellation — it kills the ticket's agents — and it
// fires from outside the marker bus, so it took a card off the board from ANY
// state and left nothing behind. Of every closed PM ticket on this board (382,
// measured 2026-08-08), 60 were closed out of a non-terminal state and 4 out of
// a RUNNING one, where a live agent was killed with no trace on the thread
// (SC-4151 E10). The stop itself is unchanged; this is the record it never left.
const RunCancelledHeader = "[human:" + MarkerRunCancelled + "]"

// RelatedStartedHeader / RelatedHeader bracket the filing-time related-work
// triage (SC-2405). Both are deliberately kept OUT of orderedMarkerSpecs, like
// PlanCommentHeader: the run is advisory and must never move the card between
// pipeline columns — the only thing that gates the card is a real dependency
// link, which refuseIfBlocked already enforces. RelatedHeader carries a head
// token: "found" (related work linked), "none" (searched, nothing found), or
// "incomplete" (the run could not finish). found/none are terminal-complete;
// incomplete is a visible record that still invites a manual re-run.
const RelatedStartedHeader = "[human:related-started]"
const RelatedHeader = "[human:related]"

// ShippedPartialHeader marks the durable shipped-partial trace (SC-2910): the
// PM ticket shipped with one or more acceptance criteria deliberately deferred
// to a follow-on ticket. Like RelatedHeader and CloseFailedHeader it is
// deliberately kept OUT of orderedMarkerSpecs — it records a partial-delivery
// decision and decorates the card, it does NOT move the card between pipeline
// columns (the real stage markers still own the card's stage). The closing
// bracket keeps "[human:shipped-partial]" from prefix-matching any other header.
const ShippedPartialHeader = "[human:shipped-partial]"

// ShippedPartialMarkerType is the value marker.ParseBody puts in .Type for a
// [human:shipped-partial] comment (the header name without the human: prefix),
// so DeriveBoardCard reads the marker via marker.Latest keyed on this const
// rather than a bare literal.
const ShippedPartialMarkerType = "shipped-partial"

// hasCompletedRelatedRecord reports whether the ticket already carries a
// COMPLETED related-work record (head "found" or "none"). An "incomplete"
// record does not count, so a died-halfway run can be re-run from the card menu.
// The closing bracket on RelatedHeader keeps "[human:related]" from matching a
// "[human:related-started]" body, exactly as it keeps plan from matching
// plan-ready.
func hasCompletedRelatedRecord(comments []tracker.Comment) bool {
	for _, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(trimmed, RelatedHeader) {
			continue
		}
		// marker.Head rather than slicing line 0: the head token is the marker
		// grammar's, and only the marker package should know where it sits.
		head := marker.Head(trimmed)
		if head == "found" || head == "none" {
			return true
		}
	}
	return false
}

// BugVerdictHeader marks the triage verdict comment both the autofix and the
// security-fix pipelines post ([human:bug-verdict] confirmed|not-a-bug|
// undetermined). It is the ticket's permanent root-cause record, NOT a stage
// transition, so — like PlanCommentHeader — it is deliberately kept OUT of
// orderedMarkerSpecs and ClassifyMarker never classifies it. Its presence is
// the resumable-run signal a recovery relaunch reads: a recorded cause with no
// [human:plan] means a self-planning fix pipeline was interrupted mid-run, a
// place to resume from rather than a ticket to refuse (SC-2986).
const BugVerdictHeader = "[human:bug-verdict]"

// BugVerifyHeader marks the verify stage's done-gate verdict
// ([human:bug-verify] DONE|NOT DONE). Like BugVerdictHeader it is the run's
// permanent evidence, NOT a stage transition, and it is deliberately kept out of
// orderedMarkerSpecs: both verdicts leave the item inside the fix run. DONE
// leads to the handoff, which is the marker that actually moves the card, and
// NOT DONE loops back to the fix while the retry budget holds.
//
// It had no constant here at all until the pipeline-machine conformance test
// asked for one — it existed only as a validation spec in internal/marker, which
// is how it ended up being a marker the prompts post eight times over and the
// board had never been told about in either direction.
const BugVerifyHeader = "[human:bug-verify]"

// hasBugVerdict reports whether the ticket carries a triage verdict comment —
// the marker heuristic a recovery relaunch falls back on when no ticket-kind
// Getter is wired. The closing bracket keeps it from matching an unrelated
// prefix, exactly as it does for plan vs plan-ready.
func hasBugVerdict(comments []tracker.Comment) bool {
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), BugVerdictHeader) {
			return true
		}
	}
	return false
}

// PipelineStartedHeader records which self-planning fix pipeline a run belongs to,
// written by the run at the moment it starts its implementation stage
// ([human:pipeline]\nkind: fix | security). Like BugVerdictHeader it is content,
// not a stage transition — deliberately kept OUT of orderedMarkerSpecs so
// ClassifyMarker never classifies it and it never moves the card. It is the
// durable pipeline-identity a recovery relaunch reads FIRST, so a fix run
// restarted after a crash is restarted as a fix and never refused for having no
// plan (SC-2989). A plan executor writes none: its identity is the absence of
// this marker plus the ticket kind.
const PipelineStartedHeader = "[human:" + MarkerPipeline + "]"

// recordedPipeline reports the pipeline identity a run wrote at start, or fixNone
// when the ticket carries no [human:pipeline] marker (a plan executor, or a run
// that predates this record). The last marker wins. The closing bracket keeps the
// header from prefix-matching an unrelated body, exactly as for plan vs plan-ready.
func recordedPipeline(comments []tracker.Comment) fixPipeline {
	kind := ""
	for _, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(trimmed, PipelineStartedHeader) {
			continue
		}
		for _, line := range strings.Split(trimmed, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "kind:"); ok {
				kind = strings.TrimSpace(v)
			}
		}
	}
	switch kind {
	case "fix":
		return fixBug
	case "security":
		return fixSecurity
	default:
		return fixNone
	}
}

// HandoffCheckUnreadableHeader flags that a board handoff's branch- or
// commit-presence check could not be PERFORMED on this machine (an unresolvable
// project dir, a git error, or a probe that ran past its timeout) — as opposed
// to a check that ran and found the commits genuinely absent. Like
// CloseFailedHeader it is a surfaced operator signal, NOT a stage transition: it
// is deliberately kept OUT of orderedMarkerSpecs so ClassifyMarker never
// classifies it and the card keeps its current (implementation-done) state,
// leaving the durable reconcile pass to retry the check rather than reddening
// good work on an answer that was never given (SC-2403).
const HandoffCheckUnreadableHeader = "[human:" + MarkerHandoffCheckUnreadable + "]"

// LateResultReconciledHeader marks the informational record described at
// MarkerLateResultReconciled. Like HandoffCheckUnreadableHeader it is
// deliberately kept OUT of orderedMarkerSpecs: it explains a contradiction
// already resolved by SC-910 supersession, never moves the card.
const LateResultReconciledHeader = "[human:" + MarkerLateResultReconciled + "]"

// daemonMarkerTypes is every marker type the daemon itself writes. It is the
// completeness list TestDaemonPostedMarkersSatisfyTheirContract walks: a type
// added here with no case in that test fails the build, which is what keeps a
// new daemon marker from shipping without anyone checking it against the
// protocol's own validator.
var daemonMarkerTypes = []string{
	MarkerDeployed,
	MarkerDeployFailed,
	MarkerNeedsPlanning,
	MarkerPRReviewFailed,
	MarkerReviewFailed,
	MarkerPipeline,
	MarkerOptions,
	MarkerOptionChosen,
	MarkerStageWait,
	MarkerClaim,
	MarkerCloseFailed,
	MarkerRunCancelled,
	MarkerPlanningFailed,
	MarkerImplementationFailed,
	MarkerPlanningOutage,
	MarkerImplementationOutage,
	MarkerReviewOutage,
	MarkerDeployOutage,
	MarkerPRReviewStarted,
	MarkerDeployFixStarted,
	MarkerHandoffCheckUnreadable,
	MarkerLateResultReconciled,
}

// markerSpec maps a marker header to the (stage, state) it represents.
type markerSpec struct {
	Header string
	Stage  BoardStage
	State  BoardState
}

// orderedMarkerSpecs lists every recognized marker. Order is not significant
// for classification (each header is unique); stage-precedence is resolved via
// stageRank ("furthest stage wins").
var orderedMarkerSpecs = []markerSpec{
	{TicketReviewStartedHeader, BoardBacklog, BoardRunning},
	{TicketReviewedHeader, BoardBacklog, BoardDone},
	{PlanningStartedHeader, BoardPlanning, BoardRunning},
	{PlanReadyHeader, BoardPlanning, BoardDone},
	{PlanningFailedHeader, BoardPlanning, BoardFailed},
	{NothingToDoHeader, BoardPlanning, BoardResolved},
	{ImplementationStartedHeader, BoardImplementation, BoardRunning},
	{ReadyForReviewHeader, BoardImplementation, BoardDone},
	{ImplementationFailedHeader, BoardImplementation, BoardFailed},
	// A refused implementation launch surfaces as a failed PLANNING card (not a
	// failed implementation one) so the planning gesture can retrigger the plan.
	{NeedsPlanningHeader, BoardPlanning, BoardFailed},
	{NoFixNeededHeader, BoardImplementation, BoardResolved},
	{ReviewStartedHeader, BoardVerification, BoardRunning},
	{ReviewCompleteHeader, BoardVerification, BoardDone},
	{ReviewFailedHeader, BoardVerification, BoardFailed},
	{PRStartedHeader, BoardDoneStage, BoardRunning},
	{PRPushedHeader, BoardDoneStage, BoardDone},
	{PRFailedHeader, BoardDoneStage, BoardFailed},
	{DeployStartedHeader, BoardDoneStage, BoardRunning},
	{DeployedHeader, BoardDoneStage, BoardDone},
	{DeployFailedHeader, BoardDoneStage, BoardFailed},
	{PRReviewStartedHeader, BoardDoneStage, BoardRunning},
	{PRFixStartedHeader, BoardDoneStage, BoardRunning},
	{PRReviewPassedHeader, BoardDoneStage, BoardRunning},
	{PRReviewFailedHeader, BoardDoneStage, BoardFailed},
	{DeployFixStartedHeader, BoardDoneStage, BoardRunning},
	{PlanningOutageHeader, BoardPlanning, BoardOutage},
	{ImplementationOutageHeader, BoardImplementation, BoardOutage},
	{ReviewOutageHeader, BoardVerification, BoardOutage},
	{DeployOutageHeader, BoardDoneStage, BoardOutage},
}

// stageRank orders the pipeline stages so derivation can pick the furthest
// stage a ticket has reached. Hidden is not ranked (handled separately).
var stageRank = map[BoardStage]int{
	BoardIdeas:          0,
	BoardBacklog:        1,
	BoardPlanning:       2,
	BoardImplementation: 3,
	BoardVerification:   4,
	BoardDoneStage:      5,
}

// DaemonLinePrefix is the LEGACY marker-body line that carried the posting
// daemon's id as a trailing line. Provenance is now a structured `machine:`
// field in the marker's field block (marker.Sign), but markers posted before
// that change still live on tickets, so this prefix is kept as a READ constant
// for the back-compat path in ParseDaemonID. Nothing writes it any more.
const DaemonLinePrefix = "daemon:"

// ParseDaemonID returns the id of the machine that posted a marker, or "" when
// the body carries no provenance (a human comment, or an agent post from before
// signing existed — claimWon already tolerates "").
//
// New markers carry provenance as the structured `machine:` field; legacy
// markers already on live tickets carry it as a trailing `daemon:` line. Reading
// the field first and falling back to the line keeps claim arbitration correct
// across the format change with no ticket migration.
func ParseDaemonID(body string) string {
	if m, ok := marker.ParseBody(body); ok {
		if id := strings.TrimSpace(m.Fields[marker.MachineField]); id != "" {
			return id
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), DaemonLinePrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// ParseBuild returns the build string a signed marker was posted from, or "" for
// a marker that carries no build field (a legacy marker, or one posted by an
// unstamped binary). It answers "which build wrote this record" the same way
// ParseDaemonID answers "which machine".
func ParseBuild(body string) string {
	if m, ok := marker.ParseBody(body); ok {
		return strings.TrimSpace(m.Fields[marker.BuildField])
	}
	return ""
}

// ClassifyMarker reports the stage and state a comment body represents and
// whether it is a recognized board marker at all. A body is only a marker when
// it STARTS with a known header (after trimming), so a quoted header in the
// middle of a discussion comment does not register. Pure: no I/O.
func ClassifyMarker(body string) (BoardStage, BoardState, bool) {
	trimmed := strings.TrimSpace(body)
	for _, spec := range orderedMarkerSpecs {
		if strings.HasPrefix(trimmed, spec.Header) {
			return spec.Stage, spec.State, true
		}
	}
	return "", "", false
}
