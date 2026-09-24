package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestParseFindings_readsEveryBlockingLineWithItsClass(t *testing.T) {
	text := "Two problems.\n\n" +
		"- BLOCKING internal/daemon/x.go:10 — user input reaches the log — [security] the key is logged raw\n" +
		"- BLOCKING internal/daemon/y.go:3 — caller has no test — [tests] nothing pins the new branch\n" +
		"Non-blocking: naming."
	got := ParseFindings(text)
	require.Len(t, got, 2)
	assert.Equal(t, Finding{File: "internal/daemon/x.go", Slug: "user input reaches the log", Class: "security", Text: "the key is logged raw"}, got[0])
	assert.Equal(t, "internal/daemon/x.go — user input reaches the log", got[0].Fingerprint())
	assert.Equal(t, "internal/daemon/x.go — security", got[0].ClassKey())
	assert.Equal(t, "tests", got[1].Class)
}

// A finding without the bracketed class is the older shape: it parses, it
// fingerprints, and it has no class key — so it can never repeat by class,
// the way a finding without a fingerprint never repeats at all.
func TestParseFindings_missingClassIsNotAnIdentity(t *testing.T) {
	got := ParseFindings("BLOCKING a.go:1 — alpha — the explanation has [brackets] later")
	require.Len(t, got, 1)
	assert.Equal(t, "", got[0].Class)
	assert.Equal(t, "", got[0].ClassKey())
	assert.Equal(t, "the explanation has [brackets] later", got[0].Text)
	got = ParseFindings("BLOCKING a.go:1 — alpha — [not a token] text")
	assert.Equal(t, "", got[0].Class, "a class is one token; a bracketed phrase is explanation")
	// A bracketed ticket reference has no internal space, so the old check
	// (reject only whitespace) let it through as a class — "sc-5174" landing
	// in criterion 4's per-class counts as if it were a real vocabulary
	// member (SC-5278).
	got = ParseFindings("BLOCKING a.go:1 — alpha — [SC-5174] regression")
	assert.Equal(t, "", got[0].Class, "a class is letters and hyphens only, never digits")
	assert.Equal(t, "[SC-5174] regression", got[0].Text)
	assert.Empty(t, ParseFindings("no blocking issues"))
}

// The bound this ticket closes: one defect reported under three slugs cost
// three rounds. Same file and same class as the finding the fixer was sent is
// repeated; the same class in a different file is a different finding.
func TestClassRepeated_sameClassSameFileUnderAFreshSlug(t *testing.T) {
	base := time.Unix(1000, 0)
	sent := ParseFindings("BLOCKING a.go:10 — key logged raw — [security] x")[0]
	comments := []tracker.Comment{cmt(prFixStartedBody(sent.Fingerprint(), sent.ClassKey()), base)}

	again := ParseFindings("BLOCKING a.go:14 — token reaches the log unescaped — [security] y")[0]
	assert.NotEqual(t, sent.Fingerprint(), again.Fingerprint(), "a fresh slug is not the same identity")
	assert.True(t, classRepeated(comments, again.ClassKey()), "but the same class in the same file is the same defect")

	elsewhere := ParseFindings("BLOCKING b.go:1 — key logged raw — [security] z")[0]
	assert.False(t, classRepeated(comments, elsewhere.ClassKey()), "the same class in another file is a new finding")
	assert.False(t, classRepeated(comments, ""), "no class, no repeat")
	assert.Equal(t, "a.go — security", lastFixClass(comments))
	assert.Equal(t, "", lastFixClass([]tracker.Comment{cmt(prFixStartedBody("a.go — slug", ""), base)}), "a marker without the field reads as no class")
}

// Through the executor: round 2 reports the same class in the same file under
// a new slug. The loop escalates instead of dispatching a third fixer, and the
// reason names the file and class — the thing that actually repeated.
func TestAdvancePRLoop_sameClassTwiceEscalatesNamingTheClass(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	sent := ParseFindings("BLOCKING internal/daemon/x.go:10 — key logged raw — [security] x")[0]
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
		{Body: prFixStartedBody(sent.Fingerprint(), sent.ClassKey()), ID: "2", Created: base.Add(2 * time.Second)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "3", Created: base.Add(3 * time.Second)},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	again := ParseFindings("BLOCKING internal/daemon/x.go:14 — token reaches the log unescaped — [security] y")[0]

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, ReviewFinding: again.Fingerprint(), ReviewClass: again.ClassKey()}))

	failed, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "the same class twice is the reviewer and fixer disagreeing")
	assert.Contains(t, failed, "same class of blocking problem twice")
	assert.Contains(t, failed, "internal/daemon/x.go — security")
	assert.Zero(t, l.calls)
}

// A new class in the same file is progress: the fixer is dispatched and the
// marker records both identities for the next round to compare.
func TestAdvancePRLoop_newClassSameFileDispatchesFixerWithClass(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	sent := ParseFindings("BLOCKING internal/daemon/x.go:10 — key logged raw — [security] x")[0]
	c := &fakeCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", ID: "0", Created: base},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "1", Created: base.Add(time.Second)},
		{Body: prFixStartedBody(sent.Fingerprint(), sent.ClassKey()), ID: "2", Created: base.Add(2 * time.Second)},
		{Body: "[human:pr-review-started]\npr: u\nnumber: 7\nbranch: feat/x", ID: "3", Created: base.Add(3 * time.Second)},
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	next := ParseFindings("BLOCKING internal/daemon/x.go:40 — new branch untested — [tests] y")[0]

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1",
		PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true, ReviewFinding: next.Fingerprint(), ReviewClass: next.ClassKey()}))

	started, ok := posted(c, PRFixStartedHeader)
	require.True(t, ok)
	assert.Contains(t, started, "finding: internal/daemon/x.go — new branch untested")
	assert.Contains(t, started, "class: internal/daemon/x.go — tests")
	assert.Equal(t, 1, l.calls)
}

// The reader of the findings record must normalize a path exactly as the
// writer did, or a query on a capitalised path — or on `file.go:42` — matches
// nothing and answers "no prior findings" forever (SC-5398).
func TestNormalizeFindingFile_matchesTheWriter(t *testing.T) {
	got := ParseFindings("BLOCKING Internal/Daemon/Foo.go:42 — slug — [tests] x")
	require.Len(t, got, 1)
	assert.Equal(t, "internal/daemon/foo.go", got[0].File)
	assert.Equal(t, got[0].File, NormalizeFindingFile("Internal/Daemon/Foo.go"))
	assert.Equal(t, got[0].File, NormalizeFindingFile("internal/daemon/foo.go:42"))
	assert.Equal(t, got[0].File, NormalizeFindingFile("  Internal/Daemon/Foo.go  "))
}
