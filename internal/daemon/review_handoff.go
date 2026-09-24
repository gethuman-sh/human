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

// ParseCommitsFromHandoff extracts the short SHAs listed on the `commits:` line
// of a [human:ready-for-review] comment body. Returns nil when the body is not a
// handoff block or carries no commits line. The commits are the SHAs a reviewer
// or the deploy binds against; verifying they exist on the branch is what keeps
// a handoff from naming commits that live nowhere but a local checkout (735).
//
// Like ParseEngineeringKeysFromHandoff, the body must START with
// ReadyForReviewHeader so a comment merely quoting the header does not register.
func ParseCommitsFromHandoff(body string) []string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, ReadyForReviewHeader) {
		return nil
	}
	for line := range strings.SplitSeq(trimmed, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "commits:")
		if !ok {
			continue
		}
		var commits []string
		for sha := range strings.SplitSeq(rest, ",") {
			if sha = strings.TrimSpace(sha); sha != "" {
				commits = append(commits, sha)
			}
		}
		return commits
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

// HandoffReviewInline is the one value the handoff's optional `review:` line
// takes: the run that posted the handoff is reviewing the work itself, in its
// own container.
//
// The field exists because the handoff is the board's "implementation
// finished, nothing is reviewing this" signal, and a board fix run publishes it
// seconds before its own [human:review-started] contradicts it — three seconds
// on SC-5396, long enough for the daemon to start a reviewer of its own. No
// claim can arbitrate that: the in-container reviewer posts none, so claimWon
// never sees it as a participant. The fact therefore has to travel on the
// signal the daemon acts on rather than on a marker that has not landed yet
// (SC-5476).
//
// Its ABSENCE is the original contract — chain a reviewer — so every handoff
// written before this existed, and every handoff human-executor-agent posts,
// keeps meaning exactly what it meant.
const HandoffReviewInline = "inline"

// ParseReviewFromHandoff extracts the value of the optional `review:` line of a
// [human:ready-for-review] comment body. Returns "" when the body is not a
// handoff block or carries no review: line.
func ParseReviewFromHandoff(body string) string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, ReadyForReviewHeader) {
		return ""
	}
	for line := range strings.SplitSeq(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "review:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// HandoffReviewsItself reports that the handoff body says its poster is
// reviewing the work itself.
func HandoffReviewsItself(body string) bool {
	return strings.EqualFold(ParseReviewFromHandoff(body), HandoffReviewInline)
}

// IsReviewComplete reports whether the comment body is a review-complete
// follow-up, which supersedes any earlier handoff for the same engineering
// keys.
func IsReviewComplete(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), ReviewCompleteHeader)
}
