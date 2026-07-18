package daemon

import "strings"

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
)

// Board marker headers. These mirror the existing review-handoff headers in
// review_handoff.go and follow the same `strings.HasPrefix(trimmed, header)`
// contract: a comment that merely quotes a header mid-body is NOT a marker.
//
// ReadyForReviewHeader is reused as the implementation done-marker and
// ReviewCompleteHeader as the verification done-marker; both are declared in
// review_handoff.go and intentionally NOT redeclared here.
const (
	PlanningStartedHeader       = "[human:planning-started]"
	PlanReadyHeader             = "[human:plan-ready]"
	PlanningFailedHeader        = "[human:planning-failed]"
	ImplementationStartedHeader = "[human:implementation-started]"
	ImplementationFailedHeader  = "[human:implementation-failed]"
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
	ReviewFailedHeader  = "[human:review-failed]"
	PRStartedHeader     = "[human:pr-started]"
	PRPushedHeader      = "[human:pr-pushed]"
	PRFailedHeader      = "[human:pr-failed]"

	// Deploy markers supersede the PR markers for the done stage: dropping a
	// card on Deploy runs push → PR → CI gate → merge → close, so the stage's
	// lifecycle is "deploying", not "opening a PR". The PR markers stay
	// recognized so threads written before the deploy pipeline still derive.
	DeployStartedHeader = "[human:deploy-started]"
	DeployedHeader      = "[human:deployed]"
	DeployFailedHeader  = "[human:deploy-failed]"
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
const CloseFailedHeader = "[human:close-failed]"

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
	{PlanningStartedHeader, BoardPlanning, BoardRunning},
	{PlanReadyHeader, BoardPlanning, BoardDone},
	{PlanningFailedHeader, BoardPlanning, BoardFailed},
	{NothingToDoHeader, BoardPlanning, BoardResolved},
	{ImplementationStartedHeader, BoardImplementation, BoardRunning},
	{ReadyForReviewHeader, BoardImplementation, BoardDone},
	{ImplementationFailedHeader, BoardImplementation, BoardFailed},
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
