package daemon

import (
	"strings"
)

// ReadyForReviewHeader is the single-line magic header that identifies a
// review handoff in a PM-ticket comment. See cli/CLAUDE.md "Review handoff".
const ReadyForReviewHeader = "[human:ready-for-review]"

// ReviewCompleteHeader is the matching follow-up header posted after the
// reviewer agent finishes. Presence of a *newer* review-complete comment
// clears the ready-for-review flag, so the TUI stops showing (R) once a
// review has actually landed.
const ReviewCompleteHeader = "[human:review-complete]"

// ParseEngineeringKeysFromHandoff extracts the engineering ticket keys listed
// on the `engineering:` line of a [human:ready-for-review] comment body.
// Returns nil if the body is not a handoff block or has no engineering line.
//
// The comment body must START with ReadyForReviewHeader so a comment that
// merely quotes the header (e.g. in a discussion) does not trigger a handoff.
func ParseEngineeringKeysFromHandoff(body string) []string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, ReadyForReviewHeader) {
		return nil
	}
	for line := range strings.SplitSeq(trimmed, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "engineering:")
		if !ok {
			continue
		}
		var keys []string
		for k := range strings.SplitSeq(rest, ",") {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
		return keys
	}
	return nil
}

// ParsePRFromHandoff extracts the pull-request URL from the optional `pr:`
// line of a [human:ready-for-review] comment body. Returns "" when the body is
// not a handoff block or carries no pr: line (the line is optional — handoffs
// from flows that only push a branch omit it).
func ParsePRFromHandoff(body string) string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, ReadyForReviewHeader) {
		return ""
	}
	for line := range strings.SplitSeq(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "pr:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// IsReviewComplete reports whether the comment body is a review-complete
// follow-up, which supersedes any earlier handoff for the same engineering
// keys.
func IsReviewComplete(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), ReviewCompleteHeader)
}
