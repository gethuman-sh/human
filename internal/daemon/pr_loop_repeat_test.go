package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

func TestFindingFingerprint(t *testing.T) {
	report := "Round-1's blocker is fixed.\n\nBLOCKING — internal/daemon/x.go:10   the guard fires only in round 1.\n- detail\nNon-blocking: more"
	got := FindingFingerprint(report)
	assert.Equal(t, "blocking — internal/daemon/x.go:10 the guard fires only in round 1.", got)
	assert.Equal(t, "no blocking issues", FindingFingerprint("\n  no blocking issues  \n"), "without a BLOCKING line the first line is the identity")
	assert.Equal(t, "", FindingFingerprint("  \n"))
	long := "BLOCKING " + strings.Repeat("x", 300)
	assert.Len(t, []rune(FindingFingerprint(long)), 160)
}

// The loop's own record of what it sent the fixer: the fingerprint rides on the
// pr-fix-started marker and reads back; a bare header (older threads) reads as
// no finding, which can never count as repeated.
func TestLastFixFinding(t *testing.T) {
	base := time.Unix(1000, 0)
	assert.Equal(t, "", lastFixFinding([]tracker.Comment{cmt(PRFixStartedHeader, base)}))
	comments := []tracker.Comment{
		cmt(prFixStartedBody("first problem"), base),
		cmt(prFixStartedBody("second problem"), base.Add(time.Minute)),
	}
	assert.Equal(t, "second problem", lastFixFinding(comments))
	assert.True(t, findingRepeated(comments, "second problem"))
	assert.False(t, findingRepeated(comments, "first problem"), "only the finding the fixer was last sent counts")
	assert.False(t, findingRepeated(comments, ""), "an empty finding is never a repeat")
}

// A review that keeps finding NEW problems is converging and keeps going; the
// same finding surviving a fix round ends it. The round count is only the
// outer cap.
func TestNextPRLoopAction_repetitionIsTheBound(t *testing.T) {
	assert.Equal(t, PRActionFix, NextPRLoopAction(PRStageReview, PRVerdictChanges, 5, DefaultPRReviewRounds, false), "round five with a new finding is progress")
	assert.Equal(t, PRActionEscalate, NextPRLoopAction(PRStageReview, PRVerdictChanges, 2, DefaultPRReviewRounds, true), "the same finding twice ends the loop at any round")
	assert.Equal(t, PRActionEscalate, NextPRLoopAction(PRStageReview, PRVerdictChanges, DefaultPRReviewRounds, DefaultPRReviewRounds, false), "the outer cap still fires")
	assert.Equal(t, PRActionMerge, NextPRLoopAction(PRStageReview, PRVerdictApproved, 7, DefaultPRReviewRounds, true), "approval merges whatever was repeated before")
}

// SC-5104's seven-round history, replayed: every round found a different real
// problem and the seventh approved. Under the round budget of three it stopped
// three times for a person; under the repetition bound it escalates at no
// round before the approval, and the marker trail names each finding sent.
func TestAdvancePRLoop_replaySC5104ConvergesWithoutAPerson(t *testing.T) {
	base := time.Now().Add(-2 * time.Hour)
	findings := []string{
		"BLOCKING — board_launch_refusal_test.go:111 was not updated for the new tryRelaunch signature",
		"BLOCKING — internal/claude/embed/shared/exit-contract.md:10 the sentence is false for two of the 19 prompts",
		"BLOCKING — internal/daemon/board_retry.go:265-271 (staleFailure) suppresses the rework build's relaunch",
		"BLOCKING — internal/daemon/board_retry.go:214 decides from a thread snapshot already invalidated",
		"BLOCKING — the round-5 fix is pinned by no test",
		"BLOCKING — a sixth, different finding",
	}
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
	}}
	l := &fakeLauncher{}
	p := &fakeDeployer{res: PRResult{Number: 7, URL: "u"}, checks: []forge.ChecksState{forge.ChecksPassing}, mergeable: true}
	deps := newDeps(c, l, p)

	for i, f := range findings {
		require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
			PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, ReviewFinding: FindingFingerprint(f)}),
			"round %d", i+1)
		_, failed := posted(c, PRReviewFailedHeader)
		require.False(t, failed, "round %d found a new problem: that is progress, not a stop", i+1)
		assert.Equal(t, "board-SC-1-prfix", l.name)
		// The fixer's round ends and the next review starts.
		require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
			PRLoopOutcome{FixExit: PRFixDone, FixRecorded: true, FixHead: fmt.Sprintf("head-%d", i)}))
		assert.Equal(t, "board-SC-1-prreview", l.name)
	}
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewHead: "final"}))

	_, passed := posted(c, PRReviewPassedHeader)
	assert.True(t, passed, "the seventh review approved and the loop merged")
	assert.Equal(t, 1, p.merged)
	assert.Equal(t, 7, prReviewRounds(c.comments))
}

// The case the bound exists for: the fixer was sent a finding and the next
// review reports the same one. The loop stops there and says which finding.
func TestAdvancePRLoop_sameFindingTwiceEscalatesAndNamesIt(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	finding := FindingFingerprint("BLOCKING — internal/daemon/x.go:10 the guard fires only in round 1")
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
		{Body: prFixStartedBody(finding), ID: "2", Created: base.Add(2 * time.Second)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "3", Created: base.Add(3 * time.Second)},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, ReviewFinding: finding}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "the same finding twice is the reviewer and fixer disagreeing")
	assert.Contains(t, failed, "same blocking problem twice")
	assert.Contains(t, failed, "x.go:10")
	assert.Zero(t, l.calls, "no third fixer for a finding the fixer did not resolve")
}

// The production shape blocking-3 describes: a FIX-stage drive still carries
// the last review's ReviewFinding (advancePRLoopFunc reads it unconditionally
// off stage.pr-review, and it is the very value that launched this fixer), so
// naively comparing it here would ALWAYS read as repeated. A fixer that
// crashed with no recorded exit must escalate on that — not on a finding the
// loop never re-reviewed.
func TestAdvancePRLoop_fixStageNeverReadsFindingAsRepeated(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	finding := FindingFingerprint("BLOCKING — internal/daemon/x.go:10 the guard fires only in round 1")
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
		{Body: prFixStartedBody(finding), ID: "2", Created: base.Add(2 * time.Second)},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	// The fixer died before writing anything: FixRecorded false, the same
	// ReviewFinding the daemon always carries forward from the last review.
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewFinding: finding, FixRecorded: false, Agent: "board-SC-1-prfix"}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "an unrecorded fix-stage exit still escalates")
	assert.NotContains(t, failed, "same blocking problem twice",
		"the finding the fixer was JUST sent must never read back as a repeat of itself")
	assert.Contains(t, failed, "stopped before recording",
		"the real cause — no exit recorded — must be the one named")
}
