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

	assert.Equal(t, "approved", readPRReviewVerdict(context.Background(), "SC-1", zerolog.Nop()))
}

// A missing report is not an error the loop can act on — it reads as "".
func TestReadPRReviewVerdict_missingIsEmpty(t *testing.T) {
	isolateState(t)
	assert.Equal(t, "", readPRReviewVerdict(context.Background(), "SC-1", zerolog.Nop()))
}

func TestReadPRFixExit_readsField(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"done","pushed":true}`)

	assert.Equal(t, "done", readPRFixExit(context.Background(), "SC-1", zerolog.Nop()))
}

func TestReadPRFixExit_needsInput(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix", `{"exit":"needs-input","pushed":false}`)

	assert.Equal(t, "needs-input", readPRFixExit(context.Background(), "SC-1", zerolog.Nop()))
}
