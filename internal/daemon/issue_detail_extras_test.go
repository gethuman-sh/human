package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// SC-365 regression: the board detail panel must surface comment-sourced
// review findings, failure reason, and fix summary. These tests exercise the
// pure parsers and the extras builder that the daemon getter now threads onto
// the wire. They fail before the fix because the symbols under test do not yet
// exist (a non-compiling new test in the package is the required red state).

func TestReviewFindings_extractsFullBody(t *testing.T) {
	comments := []tracker.Comment{
		{
			Body:    ReviewCompleteHeader + "\nverdict: pass\n\n## Findings\nNil deref in foo",
			Created: time.Now(),
		},
	}
	got := reviewFindings(comments)
	assert.Contains(t, got, "## Findings")
	assert.Contains(t, got, "Nil deref in foo")
	assert.NotContains(t, got, ReviewCompleteHeader)
}

func TestReviewFindings_latestWins(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	comments := []tracker.Comment{
		{Body: ReviewCompleteHeader + "\nold findings", Created: older},
		{Body: ReviewCompleteHeader + "\nnew findings", Created: newer},
	}
	assert.Equal(t, "new findings", reviewFindings(comments))
}

func TestReviewFindings_absent(t *testing.T) {
	comments := []tracker.Comment{{Body: "just a regular comment", Created: time.Now()}}
	assert.Equal(t, "", reviewFindings(comments))
}

func TestFixSummary_extractsBody(t *testing.T) {
	comments := []tracker.Comment{
		{Body: FixSummaryHeader + "\n## What happened\nfixed the nil deref", Created: time.Now()},
	}
	got := fixSummary(comments)
	assert.Contains(t, got, "## What happened")
	assert.Contains(t, got, "fixed the nil deref")
	assert.NotContains(t, got, FixSummaryHeader)
}

func TestFixSummary_absent(t *testing.T) {
	assert.Equal(t, "", fixSummary(nil))
}

func TestFailureReason_fromLatestFailedMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Body: ReviewFailedHeader + "\npanic in board_state.go", Created: time.Now()},
	}
	assert.Equal(t, "panic in board_state.go", latestFailureReason(comments))
}

// SC-620: the detail pane surfaces the whole diagnosis — headline plus the
// markdown detail block — not just the first line.
func TestFailureReason_fullDiagnosisBodyKept(t *testing.T) {
	body := ImplementationFailedHeader + "\nclaude exited with code 1: API Error\n\nagent: board-SC-1-implementation\nexit code: 1\n\nlast output:\n~~~\nboom\n~~~"
	comments := []tracker.Comment{{Body: body, Created: time.Now()}}
	got := latestFailureReason(comments)
	assert.Contains(t, got, "claude exited with code 1: API Error")
	assert.Contains(t, got, "last output:\n~~~\nboom\n~~~")
	assert.NotContains(t, got, ImplementationFailedHeader)
}

func TestFailureReason_supersededByNewerMarker(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		{Body: DeployFailedHeader + "\nmerge conflict on main", Created: t0},
		{Body: ImplementationStartedHeader, Created: t1},
	}
	assert.Empty(t, latestFailureReason(comments))
}

func TestFailureReason_newestFailureKept(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		{Body: ImplementationStartedHeader, Created: t0},
		{Body: ImplementationFailedHeader + "\ncompile error", Created: t1},
	}
	assert.Equal(t, "compile error", latestFailureReason(comments))
}

func TestBuildIssueDetailExtras_allThree(t *testing.T) {
	comments := []tracker.Comment{
		{Body: ReviewCompleteHeader + "\n## Findings\nreview text", Created: time.Now()},
		{Body: ReviewFailedHeader + "\nboom", Created: time.Now()},
		{Body: FixSummaryHeader + "\nsummary text", Created: time.Now()},
	}
	extras := BuildIssueDetailExtras(comments)
	assert.Contains(t, extras.ReviewFindings, "review text")
	assert.Equal(t, "boom", extras.FailureReason)
	assert.Contains(t, extras.FixSummary, "summary text")
}

func TestBuildIssueDetailExtras_emptyComments(t *testing.T) {
	extras := BuildIssueDetailExtras(nil)
	assert.Equal(t, IssueDetailExtras{}, extras)
}
