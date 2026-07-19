package daemon

import (
	"strings"

	"github.com/gethuman-sh/human/internal/tracker"
)

// BoardCard is the derived per-PM placement on the pipeline board. It is the
// single source of truth shared on the wire with the GUI and TUI, so neither
// re-derives from raw comments.
type BoardCard struct {
	Stage          BoardStage `json:"stage"`
	State          BoardState `json:"state"`
	EngineeringKey string     `json:"engineering_key,omitempty"`
	Branch         string     `json:"branch,omitempty"`
	PRURL          string     `json:"pr_url,omitempty"`
	Error          string     `json:"error,omitempty"`
	// HasPlan reports a [human:plan] comment on the ticket — the plan lives
	// here instead of on a separate engineering ticket (single-tracker
	// topology).
	HasPlan bool `json:"has_plan,omitempty"`
	// Verdict is the `verdict:` line of the latest [human:review-complete]
	// comment (pass / pass with notes / fail). A failing verdict keeps the
	// card out of Ready to Deploy and blocks the deploy transition; an absent
	// verdict counts as pass so threads reviewed before verdicts existed keep
	// flowing.
	Verdict string `json:"verdict,omitempty"`
	// Options is the latest unconsumed [human:options] block: a stage ended
	// in a decision and the card is waiting for a human to pick a direction.
	// Consumed (cleared) by an option-chosen comment or any later
	// stage-started marker.
	Options        []BoardOption `json:"options,omitempty"`
	OptionsContext string        `json:"options_context,omitempty"`
	OptionsStage   BoardStage    `json:"options_stage,omitempty"`
}

// VerdictFailed reports whether a review verdict blocks the card from moving
// forward. Only an explicit failing verdict blocks — absence is not failure.
func VerdictFailed(verdict string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(verdict)), "fail")
}

// DeriveBoardCard computes a PM ticket's board placement from its comment
// thread and tracker status. A closed/done ticket is always Hidden — closing
// is how work leaves the board, whatever its pipeline history. For open
// tickets the rule: the furthest stage carrying ANY marker wins; within that
// stage the latest marker (by Created) decides running/done/failed. A ticket
// with no markers sits in Backlog. Pure: no I/O.
//
// isIdea (the ticket carries an idea label, tracker.Issue.IsIdea) takes
// precedence over everything while the ticket is open: an idea sits in the
// Ideas column even if it somehow carries pipeline markers — deliberately, so
// the label is the single source of truth until promotion removes it.
func DeriveBoardCard(comments []tracker.Comment, statusType tracker.Category, isIdea bool) BoardCard {
	// The lister normally filters closed tickets, but one closed mid-session
	// (the board's own Close action, or a teammate on the tracker) can still
	// arrive here via an in-flight fetch — it must never render as open work.
	if statusType == tracker.CategoryDone || statusType == tracker.CategoryClosed {
		return BoardCard{Stage: BoardHidden}
	}

	if isIdea {
		return BoardCard{Stage: BoardIdeas}
	}

	furthest := BoardBacklog
	furthestRank := -1
	var anyMarker bool

	// First pass: find the furthest stage that any marker reaches.
	for _, c := range comments {
		stage, _, ok := ClassifyMarker(c.Body)
		if !ok {
			continue
		}
		anyMarker = true
		if r := stageRank[stage]; r > furthestRank {
			furthestRank = r
			furthest = stage
		}
	}

	_, hasPlan := latestPlanComment(comments)

	if !anyMarker {
		// No pipeline activity yet: the open ticket waits in Backlog.
		return BoardCard{Stage: BoardBacklog, HasPlan: hasPlan}
	}

	// Second pass: within the furthest stage, the latest marker decides state.
	state, latest := latestStateInStage(comments, furthest)

	card := BoardCard{Stage: furthest, State: state, HasPlan: hasPlan}
	card.EngineeringKey = firstEngineeringKey(comments)
	card.Branch = latestPrefixedLine(comments, ReadyForReviewHeader, "branch:")
	card.Verdict = latestPrefixedLine(comments, ReviewCompleteHeader, "verdict:")
	card.PRURL = latestPrefixedLine(comments, DeployedHeader, "pr:")
	if card.PRURL == "" {
		// Threads written before the deploy pipeline carry the URL on the old
		// pr-pushed marker.
		card.PRURL = latestPrefixedLine(comments, PRPushedHeader, "pr:")
	}
	if state == BoardFailed {
		card.Error = failureReason(latest.Body)
	}
	attachOpenOptions(&card, comments)
	return card
}

// failureReason extracts the human-readable reason from a *-failed marker: the
// first non-empty line after the header. Falls back to the header itself for
// markers posted without a reason, so a failed card never shows empty.
func failureReason(body string) string {
	trimmed := strings.TrimSpace(body)
	if _, after, ok := strings.Cut(trimmed, "\n"); ok {
		if reason := firstLine(after); reason != "" {
			return reason
		}
	}
	return firstLine(trimmed)
}

// failureBody returns everything after a *-failed marker's header line — the
// full diagnosis (headline plus markdown detail) for surfaces that can render
// more than one line. Falls back to failureReason so a reason-less marker
// still shows something.
func failureBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if _, after, ok := strings.Cut(trimmed, "\n"); ok {
		if rest := strings.TrimSpace(after); rest != "" {
			return rest
		}
	}
	return failureReason(body)
}

// latestStateInStage resolves the stage's state from its newest marker and
// returns that marker's comment so a failure message can be extracted.
func latestStateInStage(comments []tracker.Comment, stage BoardStage) (BoardState, tracker.Comment) {
	state := BoardIdle
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		s, st, ok := ClassifyMarker(c.Body)
		if !ok || s != stage {
			continue
		}
		if !haveLatest || c.Created.After(latest.Created) {
			latest = c
			haveLatest = true
			state = st
		}
	}
	return state, latest
}

// latestPlanComment returns the body of the newest [human:plan] comment with
// the header line stripped. The latest wins so re-planning supersedes older
// plans without editing comment history.
func latestPlanComment(comments []tracker.Comment) (string, bool) {
	var body string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(trimmed, PlanCommentHeader) {
			continue
		}
		if !haveLatest || c.Created.After(latest.Created) {
			latest = c
			haveLatest = true
			body = strings.TrimSpace(strings.TrimPrefix(trimmed, PlanCommentHeader))
		}
	}
	return body, haveLatest
}

// firstEngineeringKey resolves the engineering ticket key from the comment
// thread. Both [human:plan-ready] and [human:ready-for-review] carry an
// `engineering:` line, but ParseEngineeringKeysFromHandoff only matches the
// latter header — so scan plan-ready bodies directly as a fallback. The
// latest-by-time marker wins.
func firstEngineeringKey(comments []tracker.Comment) string {
	var key string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		var k string
		if keys := ParseEngineeringKeysFromHandoff(c.Body); len(keys) > 0 {
			k = keys[0]
		} else if strings.HasPrefix(strings.TrimSpace(c.Body), PlanReadyHeader) {
			k = parsePrefixedLine(c.Body, "engineering:")
		}
		if k == "" {
			continue
		}
		if !haveLatest || c.Created.After(latest.Created) {
			latest = c
			haveLatest = true
			key = k
		}
	}
	return key
}

// latestPrefixedLine returns the value of the given prefixed line from the
// latest comment whose body starts with header. Used for branch: (on
// ready-for-review) and pr: (on pr-pushed).
func latestPrefixedLine(comments []tracker.Comment, header, prefix string) string {
	var value string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), header) {
			continue
		}
		if !haveLatest || c.Created.After(latest.Created) {
			latest = c
			haveLatest = true
			value = parsePrefixedLine(c.Body, prefix)
		}
	}
	return value
}

// parsePrefixedLine returns the trimmed value following the first line that
// begins with prefix (e.g. "engineering:"), or "" when absent.
func parsePrefixedLine(body, prefix string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// firstLine returns the first non-empty line of a body, used as the error
// summary for a failed marker.
func firstLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
