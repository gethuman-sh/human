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
	report := "Round-1's blocker is fixed.\n\nBLOCKING internal/daemon/x.go:10 — the guard fires only in round 1 — detail\n- detail\nNon-blocking: more"
	assert.Equal(t, "internal/daemon/x.go — the guard fires only in round 1", FindingFingerprint(report))
	assert.Equal(t, "", FindingFingerprint("\n  no blocking issues  \n"), "no finding, no identity")
	assert.Equal(t, "", FindingFingerprint("  \n"))
	assert.Equal(t, "", FindingFingerprint("BLOCKING — internal/daemon/x.go:10   the guard fires only in round 1."),
		"a BLOCKING line without the three-part shape has no identity, so it can never read as repeated")
	long := "BLOCKING a.go:1 — " + strings.Repeat("x", 300) + " — y"
	assert.Len(t, []rune(FindingFingerprint(long)), 160)
}

// A reviewer that formats its findings as markdown — a heading, a bullet, a
// numbered item — is the common shape, and two DIFFERENT findings under the
// same heading must never collide on the heading (SC-5174, round 3).
func TestFindingFingerprint_markdownWrappedFindings(t *testing.T) {
	a := FindingFingerprint("## Findings\n\n- BLOCKING a.go:10 — guard fires once — detail one")
	b := FindingFingerprint("## Findings\n\n- BLOCKING b.go:99 — a totally different problem — detail two")
	assert.Equal(t, "a.go — guard fires once", a)
	assert.Equal(t, "b.go — a totally different problem", b)
	assert.Equal(t, "a.go — guard fires once", FindingFingerprint("1. BLOCKING a.go:12 — guard fires once — reworded"))
	assert.Equal(t, "a.go — guard fires once", FindingFingerprint("### BLOCKING: a.go:12 — guard fires once — reworded"))
}

// A stable non-finding first line — a preamble, a heading that happens to
// begin with the word Blocking — is not an identity: it yields "", which the
// loop never records and never compares.
func TestFindingFingerprint_nonFindingLinesAreNeverAnIdentity(t *testing.T) {
	a := FindingFingerprint("Two blocking findings below.\n- BLOCKING a.go:1 — alpha — x")
	b := FindingFingerprint("Two blocking findings below.\n- BLOCKING b.go:2 — beta — y")
	assert.NotEqual(t, a, b)
	assert.Equal(t, "", FindingFingerprint("Blocking findings:\nsomething without the shape"))
	assert.Equal(t, "", FindingFingerprint("Non-blocking: only nits"))
	assert.False(t, findingRepeated([]tracker.Comment{cmt(prFixStartedBody("", ""), time.Unix(1, 0))}, ""),
		"an empty identity on both sides is not a repeat")
}

func TestFindingFingerprint_threePartShape(t *testing.T) {
	got := FindingFingerprint("BLOCKING internal/daemon/x.go:10 — the guard fires only in round 1 — still not fixed")
	assert.Equal(t, "internal/daemon/x.go — the guard fires only in round 1", got)
}

// The invariant the repetition bound exists for: the SAME problem, reported
// again after a fix round that moved the line and reworded the explanation
// (exactly what a failed fix round produces), must fingerprint EQUAL — and a
// genuinely different problem, even at the same file, must not.
func TestFindingFingerprint_sameProblemAcrossAShiftedLineAndRewordedExplanation(t *testing.T) {
	round1 := "BLOCKING internal/daemon/pr_review_loop.go:167 — fingerprint ignores explanation — x"
	round2 := "BLOCKING internal/daemon/pr_review_loop.go:171 — fingerprint ignores explanation — the fix round shifted the line and reworded this, but it's still not fixed"
	assert.Equal(t, FindingFingerprint(round1), FindingFingerprint(round2),
		"same file, same slug, moved line and reworded explanation must still match")

	different := "BLOCKING internal/daemon/pr_review_loop.go:167 — a completely different problem — x"
	assert.NotEqual(t, FindingFingerprint(round1), FindingFingerprint(different),
		"a different slug at the same anchor must never collide")
}

// human-pr-reviewer-agent.md:82-85 reserves "Non-blocking:" for nits that must
// never be fingerprinted as the finding. A body that opens with a non-blocking
// note followed by two DIFFERENT blocking findings must fingerprint each
// finding, never the shared non-blocking preamble.
func TestFindingFingerprint_nonBlockingPreambleIsNeverTheIdentity(t *testing.T) {
	roundA := "Non-blocking: the test names are long.\n\nBLOCKING a.go:1 — alpha — x"
	roundB := "Non-blocking: the test names are long.\n\nBLOCKING b.go:9 — beta — y"
	assert.NotEqual(t, FindingFingerprint(roundA), FindingFingerprint(roundB),
		"two different blocking findings must never collide on a shared non-blocking preamble")
}

// The loop's own record of what it sent the fixer: the fingerprint rides on the
// pr-fix-started marker and reads back; a bare header (older threads) reads as
// no finding, which can never count as repeated.
func TestLastFixFinding(t *testing.T) {
	base := time.Unix(1000, 0)
	assert.Equal(t, "", lastFixFinding([]tracker.Comment{cmt(PRFixStartedHeader, base)}))
	comments := []tracker.Comment{
		cmt(prFixStartedBody("first problem", ""), base),
		cmt(prFixStartedBody("second problem", ""), base.Add(time.Minute)),
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
	finding := FindingFingerprint("BLOCKING internal/daemon/x.go:10 — the guard fires only in round 1 — still not fixed")
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
		{Body: prFixStartedBody(finding, ""), ID: "2", Created: base.Add(2 * time.Second)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "3", Created: base.Add(3 * time.Second)},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, ReviewFinding: finding}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "the same finding twice is the reviewer and fixer disagreeing")
	assert.Contains(t, failed, "same blocking problem twice")
	assert.Contains(t, failed, "x.go — the guard fires only in round 1")
	assert.Zero(t, l.calls, "no third fixer for a finding the fixer did not resolve")
}

// The production shape blocking-3 describes: a FIX-stage drive still carries
// the last review's ReviewFinding (advancePRLoopFunc reads it unconditionally
// off stage.pr-review, and it is the very value that launched this fixer), so
// naively comparing it here would ALWAYS read as repeated. FindingRepeated is
// computed only at the review stage (AdvancePRLoop), so it plays no part here
// either way — but SC-5554 changed what a crashed fixer's exit now does: the
// fixer's agent is confirmed gone with nothing recorded, which below its round
// budget is a dead step that RE-RUNS the fix round rather than escalating.
func TestAdvancePRLoop_fixStageNeverReadsFindingAsRepeated(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	finding := FindingFingerprint("BLOCKING internal/daemon/x.go:10 — the guard fires only in round 1 — still not fixed")
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
		{Body: prFixStartedBody(finding, ""), ID: "2", Created: base.Add(2 * time.Second)},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	// The fixer died before writing anything: FixRecorded false, the same
	// ReviewFinding the daemon always carries forward from the last review.
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewFinding: finding, FixRecorded: false, Agent: "board-SC-1-prfix"}))

	_, failed := posted(c, PRReviewFailedHeader)
	assert.False(t, failed, "a dead fixer below its round budget re-runs rather than escalating (SC-5554)")
	assert.Equal(t, 1, l.calls)
	assert.Equal(t, "/human-pr-fix SC-1 --pr=7 --branch=feat/x", l.prompt)
	refixed, ok := posted(c, PRFixStartedHeader)
	require.True(t, ok, "the relaunch charges a fresh pr-fix-started round")
	assert.Contains(t, refixed, "finding: "+finding,
		"the re-dispatch carries forward the SAME finding the dead round was sent")
}

// The fix-stage sibling of the escalation test above: once the fix step's own
// charged round count reaches the budget, the same dead fixer reds the card
// instead of re-running — and the reason names the death, never a repeated
// finding the loop never actually re-reviewed.
func TestAdvancePRLoop_deadFixerAtItsOwnBudgetEscalatesWithoutClaimingRepeat(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	finding := FindingFingerprint("BLOCKING internal/daemon/x.go:10 — the guard fires only in round 1 — still not fixed")
	comments := []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
	}
	for i := 0; i < DefaultPRReviewRounds; i++ {
		comments = append(comments, tracker.Comment{
			Body: prFixStartedBody(finding, ""), ID: fmt.Sprintf("fix-%d", i), Created: base.Add(time.Duration(2+i) * time.Second),
		})
	}
	c := &fakeCommenter{comments: comments}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewFinding: finding, FixRecorded: false, Agent: "board-SC-1-prfix"}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "the fix step's own round budget is spent")
	assert.Zero(t, l.calls, "no further relaunch once the budget is spent")
	assert.NotContains(t, failed, "same blocking problem twice",
		"the finding the fixer was JUST sent must never read back as a repeat of itself")
	assert.Contains(t, failed, "died before recording an exit",
		"the real cause — the fixer died, its re-runs spent — must be the one named")
}
