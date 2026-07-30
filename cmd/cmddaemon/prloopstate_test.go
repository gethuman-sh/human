package cmddaemon

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agentstate"
)

// writeRawReport writes a loop step's JSON report under its raw state name
// (stage.pr-review / stage.pr-fix), the shape the reviewer/fixer agents record.
func writeRawReport(t *testing.T, pmKey, name, value string) {
	t.Helper()
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	_, err = store.Set(context.Background(), pmKey, name, value,
		agentstate.FormatJSON, agentstate.Meta{Agent: "test"})
	require.NoError(t, err)
}

// TestReadPRReviewVerdict_readsField proves the typed-struct read ignores the
// report's non-string fields (a map[string]string unmarshal would fail on them).
func TestReadPRReviewVerdict_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-review", `{"verdict":"approved","blocking":0,"summary":"clean"}`)

	verdict, recorded := readPRReviewVerdict(context.Background(), "SC-1", zerolog.Nop())
	assert.Equal(t, "approved", verdict)
	assert.True(t, recorded)
}

// A missing report is not an error the loop can act on — it reads as "".
func TestReadPRReviewVerdict_missingIsEmpty(t *testing.T) {
	isolateState(t)
	verdict, recorded := readPRReviewVerdict(context.Background(), "SC-1", zerolog.Nop())
	assert.Equal(t, "", verdict)
	assert.False(t, recorded, "absence must be distinguishable from an empty verdict")
}

func TestReadPRFixReport_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"done","pushed":true}`)

	exit, pushed, options, summary, _ := readPRFixReport(context.Background(), "SC-1", zerolog.Nop())
	assert.Equal(t, "done", exit)
	assert.True(t, pushed, "the fixer's pushed flag must round-trip so the convergence guard can read it")
	assert.Empty(t, options)
	assert.Empty(t, summary)
}

func TestReadPRFixReport_needsInput(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"needs-input","pushed":false}`)

	exit, pushed, _, _, _ := readPRFixReport(context.Background(), "SC-1", zerolog.Nop())
	assert.Equal(t, "needs-input", exit)
	assert.False(t, pushed, "a fix that did not push must report pushed=false")
}

// The fixer's enumerated directions and its context line (deferred, else
// summary) must round-trip into the options block the escalation posts.
func TestReadPRFixReport_optionsAndDeferredContext(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix",
		`{"exit":"needs-input","options":[{"id":"1","label":"A"},{"id":"2","label":"B"}],"deferred":"blocked on X","summary":"one line"}`)

	exit, _, options, summary, _ := readPRFixReport(context.Background(), "SC-1", zerolog.Nop())
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

	_, _, _, summary, _ := readPRFixReport(context.Background(), "SC-1", zerolog.Nop())
	assert.Equal(t, "one line", summary)
}
