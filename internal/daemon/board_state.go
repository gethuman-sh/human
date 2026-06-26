package daemon

import (
	"strings"

	client "github.com/gethuman-sh/human-daemon-client"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BoardCard is the derived per-PM placement on the pipeline board. It is the
// single source of truth shared on the wire with the GUI and TUI, so neither
// re-derives from raw comments. The struct is defined by the public
// human-daemon-client contract; the daemon aliases it and DeriveBoardCard
// produces it.
type BoardCard = client.BoardCard

// DeriveBoardCard computes a PM ticket's board placement from its comment
// thread and tracker status. The rule: the furthest stage carrying ANY marker
// wins; within that stage the latest marker (by Created) decides
// running/done/failed. A ticket with no markers sits in Backlog while open and
// is Hidden once closed/done (those never entered the pipeline). Pure: no I/O.
func DeriveBoardCard(comments []tracker.Comment, statusType tracker.Category) BoardCard {
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

	if !anyMarker {
		// No pipeline activity: a closed/done PM ticket never entered the
		// board and is hidden; an open one waits in Backlog.
		if statusType == tracker.CategoryDone || statusType == tracker.CategoryClosed {
			return BoardCard{Stage: BoardHidden}
		}
		return BoardCard{Stage: BoardBacklog}
	}

	// Second pass: within the furthest stage, the latest marker decides state.
	state := BoardIdle
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		stage, st, ok := ClassifyMarker(c.Body)
		if !ok || stage != furthest {
			continue
		}
		if !haveLatest || c.Created.After(latest.Created) {
			latest = c
			haveLatest = true
			state = st
		}
	}

	card := BoardCard{Stage: furthest, State: state}
	card.EngineeringKey = firstEngineeringKey(comments)
	card.Branch = latestPrefixedLine(comments, ReadyForReviewHeader, "branch:")
	card.PRURL = latestPrefixedLine(comments, PRPushedHeader, "pr:")
	if state == BoardFailed {
		card.Error = firstLine(latest.Body)
	}
	return card
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
	for _, line := range strings.Split(body, "\n") {
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
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
