package cmddaemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agentstate"
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

	verdict, head, recorded, fresh := readPRReviewVerdict(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "approved", verdict)
	assert.Equal(t, "abc123", head, "the reviewed head feeds the convergence guard")
	assert.True(t, recorded)
	assert.True(t, fresh, "a zero notBefore has no round to anchor on, so any record found is fresh")
}

// A missing report is not an error the loop can act on — it reads as "".
func TestReadPRReviewVerdict_missingIsEmpty(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)
	verdict, head, recorded, fresh := readPRReviewVerdict(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "", verdict)
	assert.Equal(t, "", head)
	assert.False(t, recorded, "absence must be distinguishable from an empty verdict")
	assert.False(t, fresh)
}

func TestReadPRFixReport_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"done","head":"def456"}`)

	exit, options, summary, head, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "done", exit)
	assert.Empty(t, options)
	assert.Empty(t, summary)
	assert.Equal(t, "def456", head, "the post-fix head feeds the convergence guard")
}

func TestReadPRFixReport_needsInput(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"needs-input"}`)

	exit, _, _, _, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "needs-input", exit)
}

// The fixer's enumerated directions and its context line (deferred, else
// summary) must round-trip into the options block the escalation posts.
func TestReadPRFixReport_optionsAndDeferredContext(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix",
		`{"exit":"needs-input","options":[{"id":"1","label":"A"},{"id":"2","label":"B"}],"deferred":"blocked on X","summary":"one line"}`)

	exit, options, summary, _, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
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

	_, _, summary, _, _, _ := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
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

	verdict, head, recorded, fresh := readPRReviewVerdict(context.Background(), "", "SC-1", anchor, zerolog.Nop())
	<-written

	assert.True(t, recorded, "the settle backoff must pick up the delayed write")
	assert.True(t, fresh, "a write timestamped after the anchor is this round's own")
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

	verdict, _, recorded, fresh := readPRReviewVerdict(context.Background(), "", "SC-1", anchor, zerolog.Nop())

	assert.True(t, recorded, "a stale record was still found")
	assert.False(t, fresh, "a record older than the round's own anchor is never fresh")
	// The verdict is deliberately left unpopulated on a stale read — never
	// exposing a superseded value is what keeps a forgetful caller from acting
	// on it by accident; `recorded && !fresh` is what the caller (daemon.go)
	// wires through to PRLoopOutcome.ReviewStale.
	assert.Empty(t, verdict, "a stale record's fields are never populated")
}

func TestReadDeployFixExit_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix", `{"exit":"done"}`)

	exit := readDeployFixExit(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Equal(t, "done", exit)
}

// A missing deploy-fix report reads as "" — the driver treats a non-done exit,
// including absence, as red.
func TestReadDeployFixExit_missingIsEmpty(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)

	exit := readDeployFixExit(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	assert.Empty(t, exit)
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
