package cmddaemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
)

// shrinkPRLoopReadBackoff points the PR loop's state-store settle backoff at a
// near-zero step so tests exercising the retry path run fast, restoring the
// real values on cleanup so other tests are unaffected by a shared package var.
func shrinkPRLoopReadBackoff(t *testing.T) {
	t.Helper()
	origStep, origTries := prLoopReadRecheckStep, prLoopReadRecheckTries
	prLoopReadRecheckStep = time.Millisecond
	t.Cleanup(func() {
		prLoopReadRecheckStep, prLoopReadRecheckTries = origStep, origTries
	})
}

// writeRawReport writes a loop step's JSON report under its raw state name
// (stage.pr-review / stage.pr-fix), the shape the reviewer/fixer agents record.
func writeRawReport(t *testing.T, pmKey, name, value string) {
	t.Helper()
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	_, err = store.Set(context.Background(), "", pmKey, name, value,
		agentstate.FormatJSON, agentstate.Meta{Agent: "test"})
	require.NoError(t, err)
}

// TestReadPRReviewVerdict_readsField proves the typed-struct read ignores the
// report's non-string fields (a map[string]string unmarshal would fail on them).
func TestReadPRReviewVerdict_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-review", `{"verdict":"approved","blocking":0,"head":"abc123","summary":"clean"}`)

	verdict, head, _, _, _, read := readPRReviewVerdict(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "approved", verdict)
	assert.Equal(t, "abc123", head, "the reviewed head feeds the convergence guard")
	assert.True(t, read.recorded)
	assert.True(t, read.fresh, "a zero notBefore has no round to anchor on, so any record found is fresh")
}

// A missing report is not an error the loop can act on — it reads as "".
func TestReadPRReviewVerdict_missingIsEmpty(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)
	verdict, head, _, _, _, read := readPRReviewVerdict(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "", verdict)
	assert.Equal(t, "", head)
	assert.False(t, read.recorded, "absence must be distinguishable from an empty verdict")
	assert.False(t, read.fresh)
}

// A reviewer that could not reach the substrate records the exit contract's
// outage and no verdict at all. Reading only the verdict made that an empty
// verdict, which the loop escalates on (SC-5627).
func TestReadPRReviewVerdict_readsTheOutageExitAndSummary(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-review", `{"exit":"outage","summary":"the tracker API was unreachable"}`)

	verdict, _, _, exit, summary, read := readPRReviewVerdict(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Empty(t, verdict, "an outage records no verdict")
	assert.Equal(t, string(daemon.ExitOutage), exit)
	assert.Equal(t, "the tracker API was unreachable", summary, "the card's face names what was unreachable")
	assert.True(t, read.recorded)
	assert.True(t, read.fresh)
}

// deferred is the findings note the options block leads with; an outage deferred
// nothing, so the line the card needs is the summary (SC-5627).
func TestReadPRFixReport_outageLineIsTheSummaryNotTheDeferred(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix",
		`{"exit":"outage","deferred":"nothing addressed","summary":"the model API was unreachable"}`)

	exit, _, summary, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, string(daemon.ExitOutage), exit)
	assert.Equal(t, "the model API was unreachable", summary)
}

func TestReadPRFixReport_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"done","head":"def456"}`)

	exit, options, summary, head, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "done", exit)
	assert.Empty(t, options)
	assert.Empty(t, summary)
	assert.Equal(t, "def456", head, "the post-fix head feeds the convergence guard")
}

func TestReadPRFixReport_needsInput(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"needs-input"}`)

	exit, _, _, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "needs-input", exit)
}

// The fixer's enumerated directions and its context line (deferred, else
// summary) must round-trip into the options block the escalation posts.
func TestReadPRFixReport_optionsAndDeferredContext(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix",
		`{"exit":"needs-input","options":[{"id":"1","label":"A"},{"id":"2","label":"B"}],"deferred":"blocked on X","summary":"one line"}`)

	exit, options, summary, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "needs-input", exit)
	require.Len(t, options, 2)
	assert.Equal(t, "A", options[0].Label)
	assert.Equal(t, "B", options[1].Label)
	// deferred wins over summary as the human-facing context line.
	assert.Equal(t, "blocked on X", summary)
}

// With no deferred line the summary is the context fallback.
func TestReadPRFixReport_summaryContextFallback(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"needs-input","summary":"one line"}`)

	_, _, summary, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "one line", summary)
}

// TestReadPRReviewVerdict_waitsForFreshVerdict proves the settle backoff: a
// record written AFTER the anchor (this round's own write) but not visible on
// the very first read becomes visible on a re-read within the backoff window,
// and is then reported fresh — the read-after-write race SC-2307 exposed.
func TestReadPRReviewVerdict_waitsForFreshVerdict(t *testing.T) {
	isolateState(t)
	origStep, origTries := prLoopReadRecheckStep, prLoopReadRecheckTries
	prLoopReadRecheckStep = 2 * time.Millisecond
	prLoopReadRecheckTries = 10 // plenty of window for the delayed write below
	t.Cleanup(func() { prLoopReadRecheckStep, prLoopReadRecheckTries = origStep, origTries })
	anchor := time.Now()

	// Simulate the reviewer's write landing slightly after the anchor, but only
	// once the read has already fired once: write in a goroutine timed to land
	// inside the backoff window.
	written := make(chan struct{})
	go func() {
		time.Sleep(3 * prLoopReadRecheckStep)
		writeRawReport(t, "SC-1", "stage.pr-review", `{"verdict":"approved","head":"abc123"}`)
		close(written)
	}()

	verdict, head, _, _, _, read := readPRReviewVerdict(context.Background(), "", "SC-1", anchor, zerolog.Nop())
	<-written

	assert.True(t, read.recorded, "the settle backoff must pick up the delayed write")
	assert.True(t, read.fresh, "a write timestamped after the anchor is this round's own")
	assert.Equal(t, "approved", verdict)
	assert.Equal(t, "abc123", head)
}

// TestReadPRReviewVerdict_staleOnly_notFresh proves a record left over from a
// PRIOR round (written and thus timestamped before the anchor) is reported
// recorded-but-not-fresh even after the full settle backoff is spent — it must
// never be mistaken for this round's own verdict (SC-2378).
func TestReadPRReviewVerdict_staleOnly_notFresh(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)
	writeRawReport(t, "SC-1", "stage.pr-review", `{"verdict":"changes-requested","head":"abc123"}`)
	anchor := time.Now().Add(time.Hour) // anchor is "in the future" relative to the write above

	verdict, _, _, _, _, read := readPRReviewVerdict(context.Background(), "", "SC-1", anchor, zerolog.Nop())

	assert.True(t, read.recorded, "a stale record was still found")
	assert.False(t, read.fresh, "a record older than the round's own anchor is never fresh")
	assert.True(t, read.stale(), "recorded, not fresh, not a failed decode of this round's own write")
	// The verdict is deliberately left unpopulated on a stale read — never
	// exposing a superseded value is what keeps a forgetful caller from acting
	// on it by accident; `recorded && !fresh` is what the caller (daemon.go)
	// wires through to PRLoopOutcome.ReviewStale.
	assert.Empty(t, verdict, "a stale record's fields are never populated")
}

// A record that IS this round's own write and will not decode is a THIRD
// state, distinct from stale (SC-5554): the step reported something the
// daemon cannot read, rather than never having written at all.
func TestReadStageReportSettled_freshButUndecodableIsUnreadableNotStale(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-review", `{"verdict":5}`)

	var v struct {
		Verdict string `json:"verdict"`
	}
	read := readStageReportSettled(context.Background(), "", "SC-1", "stage.pr-review", time.Time{}, &v, zerolog.Nop())

	assert.True(t, read.recorded)
	assert.True(t, read.unreadable)
	assert.False(t, read.fresh)
	assert.False(t, read.stale(), "an undecodable write of THIS round is not the same failure as a prior round's leftover")
}

func TestReadDeployFixReport_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix", `{"exit":"done"}`)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, daemon.ExitDone, report.Exit)
}

// The blocker object is what the ticket exists to carry; a wrong tag would
// drop every field silently, so the decode is pinned field by field.
func TestReadDeployFixReport_readsTheBlocker(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix",
		`{"exit":"needs-human-work","blocker":{"kind":"missing-permission","evidence":"403 on push","attempted":"retried once","release":"token gains write"}}`)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, daemon.ExitNeedsHumanWork, report.Exit)
	assert.Equal(t, daemon.Blocker{Kind: "missing-permission", Evidence: "403 on push", Attempted: "retried once", Release: "token gains write"}, report.Blocker)
}

// An outage carries no blocker by contract — it has a summary instead, which is
// the only place the unreachable substrate is named (SC-5592).
func TestReadDeployFixReport_readsTheSummary(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix",
		`{"exit":"outage","summary":"the git remote was unreachable"}`)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, daemon.ExitOutage, report.Exit)
	assert.Equal(t, "the git remote was unreachable", report.Summary)
}

// A missing deploy-fix report reads as "" with Unconfirmed true — the driver
// re-runs the round rather than redding it (SC-5554). A record older than the
// round's own anchor (a previous dispatch's leftover) is the same case: this
// round confirmed nothing. An undecodable record that IS this round's own
// write is NOT unconfirmed — that fixer reported — so it falls through to red.
func TestReadDeployFixReport_missingIsUnconfirmed(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Empty(t, report.Exit)
	assert.True(t, report.Unconfirmed, "nothing recorded at all is unconfirmed")
}

func TestReadDeployFixReport_staleRecordIsUnconfirmed(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix", `{"exit":"needs-input"}`)
	anchor := time.Now().Add(time.Hour) // anchor is "in the future" relative to the write above

	report := readDeployFixReport(context.Background(), "", "SC-1", anchor, zerolog.Nop())
	assert.Empty(t, report.Exit, "a stale record's fields are never populated")
	assert.True(t, report.Unconfirmed, "a record from an earlier dispatch confirms nothing about THIS round")
}

func TestReadDeployFixReport_undecodableFreshRecordIsNotUnconfirmed(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix", `{"exit":5}`)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.False(t, report.Unconfirmed, "this round's own write, undecodable, is a step that reported — not a dead one")
}

// SC-5554 review note 1: json.Unmarshal does not stop at the first type
// error — it keeps decoding the fields that follow and returns the error only
// once the whole object is consumed. "exit" appears before "blocker" in this
// record, so it decodes to "done" before "blocker" (a string, not the object
// Blocker.UnmarshalJSON expects) fails. A reader that passed that half-decoded
// Exit through would hand AdvanceDeployFix's done arm a record the daemon
// never actually confirmed, which would publish the branch and re-run the
// deploy on it. The reader must zero every field of an undecodable record
// instead, so it falls through to the generic red exactly like any other
// recorded-but-unusable exit (TestReadDeployFixReport_undecodableFreshRecordIsNotUnconfirmed).
func TestReadDeployFixReport_undecodableRecordExposesNoHalfDecodedExit(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix", `{"exit":"done","blocker":"oops"}`)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Empty(t, report.Exit, "an undecodable record must not hand a half-decoded exit through")
	assert.False(t, report.Unconfirmed, "this round's own write, undecodable, is a step that reported — not a dead one")
}

// ctx cancellation mid-backoff must return promptly rather than block for the
// full retry budget.
func TestReadPRReviewVerdict_ctxCancelled_returnsPromptly(t *testing.T) {
	isolateState(t)
	origStep, origTries := prLoopReadRecheckStep, prLoopReadRecheckTries
	prLoopReadRecheckStep = 5 * time.Second
	prLoopReadRecheckTries = 5
	t.Cleanup(func() { prLoopReadRecheckStep, prLoopReadRecheckTries = origStep, origTries })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		readPRReviewVerdict(ctx, "", "SC-1", time.Now(), zerolog.Nop())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readPRReviewVerdict did not return promptly after ctx cancellation")
	}
}
