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
	_, err = store.Set(context.Background(), "", pmKey, name, value,
		agentstate.FormatJSON, agentstate.Meta{Agent: "test"})
	require.NoError(t, err)
}

// TestReadPRReviewVerdict_readsField proves the typed-struct read ignores the
// report's non-string fields (a map[string]string unmarshal would fail on them).
func TestReadPRReviewVerdict_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-review", `{"verdict":"approved","blocking":0,"head":"abc123","summary":"clean"}`)

	verdict, head, recorded := readPRReviewVerdict(context.Background(), "", "SC-1", zerolog.Nop())
	assert.Equal(t, "approved", verdict)
	assert.Equal(t, "abc123", head, "the reviewed head feeds the convergence guard")
	assert.True(t, recorded)
}

// A missing report is not an error the loop can act on — it reads as "".
func TestReadPRReviewVerdict_missingIsEmpty(t *testing.T) {
	isolateState(t)
	verdict, head, recorded := readPRReviewVerdict(context.Background(), "", "SC-1", zerolog.Nop())
	assert.Equal(t, "", verdict)
	assert.Equal(t, "", head)
	assert.False(t, recorded, "absence must be distinguishable from an empty verdict")
}

func TestReadPRFixReport_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"done","head":"def456"}`)

	exit, options, summary, head, _ := readPRFixReport(context.Background(), "", "SC-1", zerolog.Nop())
	assert.Equal(t, "done", exit)
	assert.Empty(t, options)
	assert.Empty(t, summary)
	assert.Equal(t, "def456", head, "the post-fix head feeds the convergence guard")
}

func TestReadPRFixReport_needsInput(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"needs-input"}`)

	exit, _, _, _, _ := readPRFixReport(context.Background(), "", "SC-1", zerolog.Nop())
	assert.Equal(t, "needs-input", exit)
}

// The fixer's enumerated directions and its context line (deferred, else
// summary) must round-trip into the options block the escalation posts.
func TestReadPRFixReport_optionsAndDeferredContext(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix",
		`{"exit":"needs-input","options":[{"id":"1","label":"A"},{"id":"2","label":"B"}],"deferred":"blocked on X","summary":"one line"}`)

	exit, options, summary, _, _ := readPRFixReport(context.Background(), "", "SC-1", zerolog.Nop())
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

	_, _, summary, _, _ := readPRFixReport(context.Background(), "", "SC-1", zerolog.Nop())
	assert.Equal(t, "one line", summary)
}
