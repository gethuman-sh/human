package daemon

import "strings"

// BoardStage is one of the five pipeline columns (plus a synthetic "hidden"
// stage for closed PM tickets that never entered the pipeline). The drag-board
// GUI renders cards into these columns; the daemon derives the stage from the
// [human:…] comment markers a PM ticket carries.
type BoardStage string

const (
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
	ReviewStartedHeader         = "[human:review-started]"
	ReviewFailedHeader          = "[human:review-failed]"
	PRStartedHeader             = "[human:pr-started]"
	PRPushedHeader              = "[human:pr-pushed]"
	PRFailedHeader              = "[human:pr-failed]"
)

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
	{ImplementationStartedHeader, BoardImplementation, BoardRunning},
	{ReadyForReviewHeader, BoardImplementation, BoardDone},
	{ImplementationFailedHeader, BoardImplementation, BoardFailed},
	{ReviewStartedHeader, BoardVerification, BoardRunning},
	{ReviewCompleteHeader, BoardVerification, BoardDone},
	{ReviewFailedHeader, BoardVerification, BoardFailed},
	{PRStartedHeader, BoardDoneStage, BoardRunning},
	{PRPushedHeader, BoardDoneStage, BoardDone},
	{PRFailedHeader, BoardDoneStage, BoardFailed},
}

// stageRank orders the pipeline stages so derivation can pick the furthest
// stage a ticket has reached. Hidden is not ranked (handled separately).
var stageRank = map[BoardStage]int{
	BoardBacklog:        0,
	BoardPlanning:       1,
	BoardImplementation: 2,
	BoardVerification:   3,
	BoardDoneStage:      4,
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
